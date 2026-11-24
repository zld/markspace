package rtprec

import (
	"testing"

	"github.com/zld/mediapipe/media"
)

func synth(n int, drop []int, reorder []int) *Sequence {
	sent := make([]uint16, n)
	packets := make([]Packet, 0, n)
	base := uint16(1000)
	for i := 0; i < n; i++ {
		sent[i] = base + uint16(i)
	}
	loss := map[int]bool{}
	for _, d := range drop {
		loss[d] = true
	}
	rorder := map[int]int{}
	for k := 0; k+1 < len(reorder); k += 2 {
		rorder[reorder[k]] = reorder[k+1]
		rorder[reorder[k+1]] = reorder[k]
	}
	for i := 0; i < n; i++ {
		if loss[i] {
			continue
		}
		wire := []byte{byte(i + 1)}
		packets = append(packets, Packet{Seq: sent[i], Arrival: len(packets), Raw: wire})
		_ = rorder
	}
	return &Sequence{SSRC: 1, ClockRate: 90000, Sent: sent, Packets: packets}
}

func TestReassembleCompleteSequence(t *testing.T) {
	seq := synth(120, nil, nil)
	r, err := Reassemble(seq, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if r.OrderedCount != 120 || r.LostCount != 0 {
		t.Fatalf("ordered=%d lost=%d", r.OrderedCount, r.LostCount)
	}
	if got := r.RecoveryRate(); got != 1.0 {
		t.Fatalf("rate=%v want 1.0", got)
	}
	for i := 1; i < len(r.Ordered); i++ {
		if r.Ordered[i].Seq != r.Ordered[i-1].Seq+1 {
			t.Fatalf("ordered output is not monotonic at %d", i)
		}
	}
	if len(r.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", r.Issues)
	}
}

func TestReassembleOutOfOrderRecovers(t *testing.T) {
	// Arrival order is 0..9, then 13..15, then the late 10..12, then 16..39.
	// The hole at 10 is bridged within the wait window, so nothing is lost.
	seq := synth(40, nil, nil)
	var before, gap, mid, tail []Packet
	for _, p := range seq.Packets {
		switch {
		case p.Seq < 1000+10:
			before = append(before, p)
		case p.Seq >= 1000+10 && p.Seq <= 1000+12:
			gap = append(gap, p)
		case p.Seq > 1000+12 && p.Seq <= 1000+15:
			mid = append(mid, p)
		default:
			tail = append(tail, p)
		}
	}
	reordered := append([]Packet{}, before...)
	reordered = append(reordered, mid...)
	reordered = append(reordered, gap...)
	reordered = append(reordered, tail...)
	for i := range reordered {
		reordered[i].Arrival = i
	}
	seq.Packets = reordered
	cfg := DefaultConfig()
	cfg.WaitPackets = 20
	r, err := Reassemble(seq, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.LostCount != 0 || len(r.Gaps) != 0 {
		t.Fatalf("expected full recovery, got lost=%d gaps=%+v", r.LostCount, r.Gaps)
	}
	if r.ReorderedCount != 3 {
		t.Fatalf("reordered=%d want 3", r.ReorderedCount)
	}
	if r.OrderedCount != 40 {
		t.Fatalf("ordered=%d want 40", r.OrderedCount)
	}
	for i, p := range r.Ordered {
		if p.Seq != 1000+uint16(i) {
			t.Fatalf("output out of order at %d: seq %d", i, p.Seq)
		}
	}
}

func TestReassembleBurstLossReported(t *testing.T) {
	seq := synth(100, []int{40, 41, 42, 43, 44, 45}, nil)
	r, err := Reassemble(seq, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if r.LostCount != 6 {
		t.Fatalf("lost=%d want 6", r.LostCount)
	}
	if len(r.Gaps) != 1 || r.Gaps[0].Count != 6 {
		t.Fatalf("gaps=%+v", r.Gaps)
	}
	want := 94.0 / 100.0
	if got := r.RecoveryRate(); got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("rate=%v want %v", got, want)
	}
	found := false
	for _, is := range r.Issues {
		if is.Kind == media.KindRTPGap && is.Severity == media.SeverityError {
			found = true
		}
	}
	if !found {
		t.Fatal("unrecoverable gap must be recorded in issues")
	}
}

func TestReassembleDropsDuplicates(t *testing.T) {
	seq := synth(40, nil, nil)
	seq.Packets = append(seq.Packets, seq.Packets[0], seq.Packets[5])
	r, err := Reassemble(seq, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if r.DuplicateCount != 2 {
		t.Fatalf("duplicates=%d", r.DuplicateCount)
	}
	if r.OrderedCount != 40 {
		t.Fatalf("ordered=%d", r.OrderedCount)
	}
}

func TestReassembleWindowForcesLoss(t *testing.T) {
	// Two missing packets early; with a tiny window and short wait they must
	// be committed as lost and never appear later.
	seq := synth(30, []int{3, 4}, nil)
	cfg := Config{Window: 4, WaitPackets: 1}
	r, err := Reassemble(seq, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.LostCount != 2 {
		t.Fatalf("lost=%d", r.LostCount)
	}
	for _, p := range r.Ordered {
		if p.Seq == 1003 || p.Seq == 1004 {
			t.Fatal("lost packet was emitted")
		}
	}
}

func TestReassembleRejectsBadConfig(t *testing.T) {
	seq := synth(10, nil, nil)
	if _, err := Reassemble(seq, Config{Window: 1, WaitPackets: 1}); err == nil {
		t.Fatal("expected window validation error")
	}
	if _, err := Reassemble(seq, Config{Window: 10, WaitPackets: 0}); err == nil {
		t.Fatal("expected wait validation error")
	}
}

func TestReassembleDeterministic(t *testing.T) {
	seq := synth(80, []int{9, 10, 11}, []int{20, 21})
	a, err := Reassemble(seq, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Reassemble(seq, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if a.OrderedCount != b.OrderedCount || a.LostCount != b.LostCount {
		t.Fatal("non-deterministic result")
	}
	for i := range a.Ordered {
		if a.Ordered[i].Seq != b.Ordered[i].Seq {
			t.Fatal("non-deterministic ordering")
		}
	}
}
