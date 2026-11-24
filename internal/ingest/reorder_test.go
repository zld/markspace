package ingest

import (
	"testing"

	"github.com/pion/rtp"
)

func rtpEv(seq uint16, arrival int64) Event {
	return Event{
		Kind:    KindRTP,
		Arrival: arrival,
		Packet:  &rtp.Packet{Header: rtp.Header{SequenceNumber: seq}},
	}
}

func committedSeqs(events []Event) []uint16 {
	var out []uint16
	for _, e := range events {
		if e.Kind == KindRTP {
			out = append(out, e.Seq())
		}
	}
	return out
}

func TestInOrderNoLoss(t *testing.T) {
	r := NewReorderer(12)
	for i := 0; i < 100; i++ {
		r.Push(rtpEv(uint16(1000+i), int64(i)))
	}
	events, st := r.Flush()
	if st.Received != 100 || st.Lost != 0 || st.Duplicates != 0 || st.Late != 0 {
		t.Fatalf("stats = %+v", st)
	}
	seqs := committedSeqs(events)
	for i, s := range seqs {
		if s != uint16(1000+i) {
			t.Fatalf("position %d seq = %d", i, s)
		}
	}
}

func TestReorderWithinWindow(t *testing.T) {
	r := NewReorderer(12)
	order := []uint16{1000, 1002, 1001, 1003, 1005, 1004, 1006}
	for i, s := range order {
		r.Push(rtpEv(s, int64(i)))
	}
	events, st := r.Flush()
	if st.Lost != 0 || st.Duplicates != 0 {
		t.Fatalf("stats = %+v", st)
	}
	want := []uint16{1000, 1001, 1002, 1003, 1004, 1005, 1006}
	got := committedSeqs(events)
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestGapMarkedOnLoss(t *testing.T) {
	r := NewReorderer(12)
	seqs := []uint16{1000, 1001, 1004, 1005, 1006, 1007, 1008, 1009, 1010,
		1011, 1012, 1013, 1014, 1015, 1016, 1017, 1018, 1019, 1020}
	for i, s := range seqs {
		r.Push(rtpEv(s, int64(i)))
	}
	_, st := r.Flush()
	if st.Lost != 2 {
		t.Fatalf("lost = %d, want 2 (1002,1003)", st.Lost)
	}
	if len(st.Gaps) != 1 || st.Gaps[0].From != 1002 || st.Gaps[0].To != 1003 {
		t.Fatalf("gaps = %+v", st.Gaps)
	}
}

func TestDuplicatesCounted(t *testing.T) {
	r := NewReorderer(12)
	for i := 0; i < 11; i++ {
		r.Push(rtpEv(uint16(1000+i), int64(i)))
	}
	r.Push(rtpEv(1005, 100)) // duplicate of still-buffered packet
	r.Push(rtpEv(1005, 101))
	for i := 11; i < 20; i++ {
		r.Push(rtpEv(uint16(1000+i), int64(i)))
	}
	_, st := r.Flush()
	if st.Duplicates != 2 {
		t.Fatalf("duplicates = %d, want 2", st.Duplicates)
	}
}

func TestLatePacketNotCommitted(t *testing.T) {
	r := NewReorderer(4)
	// push 0..19 in order; position 2 committed by the time we inject it late
	for i := 0; i < 20; i++ {
		r.Push(rtpEv(uint16(i), int64(i)))
	}
	r.Push(rtpEv(2, 100)) // late duplicate of committed position
	events, st := r.Flush()
	if st.Late != 1 {
		t.Fatalf("late = %d, want 1", st.Late)
	}
	// committed stream must be untouched: 0..19 exactly once
	seqs := committedSeqs(events)
	if len(seqs) != 20 {
		t.Fatalf("committed %d packets, want 20", len(seqs))
	}
	for i, s := range seqs {
		if s != uint16(i) {
			t.Fatalf("position %d = %d", i, s)
		}
	}
}

func TestSequenceWrap(t *testing.T) {
	r := NewReorderer(12)
	start := uint16(65530)
	n := 20
	for i := 0; i < n; i++ {
		r.Push(rtpEv(start+uint16(i), int64(i)))
	}
	events, st := r.Flush()
	if st.Lost != 0 {
		t.Fatalf("lost = %d across wrap", st.Lost)
	}
	seqs := committedSeqs(events)
	if len(seqs) != n {
		t.Fatalf("committed %d, want %d", len(seqs), n)
	}
	for i, s := range seqs {
		if s != start+uint16(i) {
			t.Fatalf("position %d seq = %d", i, s)
		}
	}
}

func TestLateArrivingLossBecomesLateNotRewrite(t *testing.T) {
	r := NewReorderer(4)
	// 1002 missing; enough later packets to commit past it
	seqs := []uint16{1000, 1001, 1003, 1004, 1005, 1006, 1007, 1008, 1009, 1010}
	for i, s := range seqs {
		r.Push(rtpEv(s, int64(i)))
	}
	// now 1002 arrives too late
	r.Push(rtpEv(1002, 200))
	events, st := r.Flush()
	if st.Late != 1 {
		t.Fatalf("late = %d, want 1", st.Late)
	}
	if st.Lost != 1 {
		t.Fatalf("lost = %d, want 1 (1002 stays lost)", st.Lost)
	}
	for _, e := range events {
		if e.Kind == KindRTP && e.Seq() == 1002 {
			t.Fatal("late packet must not rewrite committed range")
		}
	}
}
