package recover

import (
	"testing"

	"archivectl/internal/ebml"

	"github.com/pion/rtp"
)

// buildDoc assembles a small Matroska-like document with two tracks and an
// unknown-size final cluster that is truncated mid-block.
func buildDoc(t *testing.T, truncate bool) []byte {
	t.Helper()
	tc := ebml.ElementBytes(ebml.IDTimecode, ebml.UintBytes(0))
	b1 := block(1, 0, true, []byte{1, 2, 3, 4})
	b2 := block(2, 10, false, []byte{5, 6, 7})
	b3 := block(1, 40, false, []byte{8, 9, 10, 11, 12})
	var payload []byte
	payload = append(payload, tc...)
	payload = append(payload, b1...)
	payload = append(payload, b2...)
	payload = append(payload, b3...)
	cluster := ebml.ElementBytesUnknown(ebml.IDCluster, payload)
	if !truncate {
		return cluster
	}
	return cluster[:len(cluster)-3]
}


func pkt(sn uint16) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{SequenceNumber: sn}}
}

func pktWith(sn uint16, payload []byte) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{SequenceNumber: sn}, Payload: payload}
}

func block(track uint32, relTc int16, key bool, data []byte) []byte {
	var p []byte
	p = append(p, ebml.EncodeVINT(uint64(track), 0)...)
	p = append(p, byte(relTc>>8), byte(relTc))
	var flags byte
	if key {
		flags = 0x80
	}
	p = append(p, flags)
	p = append(p, data...)
	return ebml.ElementBytes(ebml.IDSimpleBlock, p)
}

func TestRebuildIndexTruncatedKeepsCompleteBlocks(t *testing.T) {
	doc := ebml.ParseDocument(buildDoc(t, true))
	idx := RebuildIndex(doc)
	if !idx.Rebuilt {
		t.Fatal("index should be marked rebuilt")
	}
	if !idx.Truncated {
		t.Fatal("truncation flag must be set")
	}
	// Two complete blocks survive; the damaged offset is recorded separately.
	if idx.Count() != 2 {
		t.Fatalf("want 2 usable entries, got %d", idx.Count())
	}
	if len(idx.DamagedBytes) == 0 {
		t.Fatal("damaged offsets must be recorded")
	}
	for _, e := range idx.Entries {
		if !idx.OffsetHealthy(e.Offset) {
			t.Fatalf("index entry at damaged offset %d treated as valid", e.Offset)
		}
	}
	// Entries must be time-ordered.
	for i := 1; i < len(idx.Entries); i++ {
		if idx.Entries[i].TimeNs < idx.Entries[i-1].TimeNs {
			t.Fatal("index entries not time-ordered")
		}
	}
}

func TestRebuildIndexHealthyDocument(t *testing.T) {
	doc := ebml.ParseDocument(buildDoc(t, false))
	idx := RebuildIndex(doc)
	if idx.Truncated {
		t.Fatal("healthy document must not be flagged truncated")
	}
	if idx.Count() != 3 {
		t.Fatalf("want 3 entries, got %d", idx.Count())
	}
}

func TestRTPReorderDuplicatesAndLoss(t *testing.T) {
	// Sequence 0..9 with 3 and 7 lost, 5/2 arrive out of order, 1 duplicated.
	seq := []uint16{0, 1, 1, 5, 2, 4, 6, 8, 9}
	pkts := make([]*rtp.Packet, len(seq))
	for i, s := range seq {
		pkts[i] = pkt(s)
	}
	res, err := RecoverRTP(pkts, 8)
	if err != nil {
		t.Fatal(err)
	}
	if res.Duplicates != 1 {
		t.Fatalf("want 1 duplicate, got %d", res.Duplicates)
	}
	if res.Expected != 10 {
		t.Fatalf("want expected range 10, got %d", res.Expected)
	}
	if res.Unrecoverable != 2 {
		t.Fatalf("want 2 unrecoverable (3,7), got %d", res.Unrecoverable)
	}
	// Packets 2,4,5,6,8,9 arrived after a higher number -> recovered.
	if res.Recovered < 1 {
		t.Fatalf("out-of-order packets should be counted recovered, got %d", res.Recovered)
	}
	// Ordered output must be strictly increasing.
	for i := 1; i < len(res.Ordered); i++ {
		if !seqLess(res.Ordered[i-1].SequenceNumber, res.Ordered[i].SequenceNumber) {
			t.Fatal("ordered output not strictly increasing")
		}
	}
	// 8 present out of 10 expected => 0.8 raw presence.
	if got := res.RecoveryRate(); got < 0.79 || got > 0.81 {
		t.Fatalf("recovery rate = %f, want ~0.8", got)
	}
}

func TestRTPWindowLimitsLossRecovery(t *testing.T) {
	// Gap at seq 1 never fills; window=1 means it is declared quickly.
	pkts := []*rtp.Packet{pkt(0), pkt(2), pkt(3)}
	res, err := RecoverRTP(pkts, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unrecoverable != 1 {
		t.Fatalf("want 1 unrecoverable gap, got %d", res.Unrecoverable)
	}
	if len(res.Ordered) != 3 {
		t.Fatalf("all three present packets must be emitted, got %d", len(res.Ordered))
	}
}

func TestRTPWindowCapEnforced(t *testing.T) {
	if _, err := RecoverRTP(nil, MaxRecoveryWindow+1); err != ErrWindowTooLarge {
		t.Fatalf("want ErrWindowTooLarge, got %v", err)
	}
}

func TestRTPPastWindowVsTailGap(t *testing.T) {
	// seq 1 missing, with 0 and 2..6 present: 5 newer sequences exist, so
	// with window=4 the gap is declared past-window; with window=8 it is a
	// tail gap. Either way it counts as one unrecoverable loss.
	var seq []uint16
	for s := uint16(0); s <= 6; s++ {
		if s == 1 {
			continue
		}
		seq = append(seq, s)
	}
	mk := func() []*rtp.Packet {
		out := make([]*rtp.Packet, len(seq))
		for i, s := range seq {
			out[i] = pkt(s)
		}
		return out
	}
	small, _ := RecoverRTP(mk(), 4)
	if small.PastWindow != 1 || small.TailGaps != 0 {
		t.Fatalf("window=4: want 1 past-window, got past=%d tail=%d",
			small.PastWindow, small.TailGaps)
	}
	large, _ := RecoverRTP(mk(), 8)
	if large.TailGaps != 1 || large.PastWindow != 0 {
		t.Fatalf("window=8: want 1 tail gap, got past=%d tail=%d",
			large.PastWindow, large.TailGaps)
	}
}

func TestRTPDeterministic(t *testing.T) {
	seq := []uint16{0, 4, 1, 3, 2, 6, 5}
	mk := func() []*rtp.Packet {
		out := make([]*rtp.Packet, len(seq))
		for i, s := range seq {
			out[i] = pktWith(s, []byte{byte(s)})
		}
		return out
	}
	r1, _ := RecoverRTP(mk(), 8)
	r2, _ := RecoverRTP(mk(), 8)
	if r1.Recovered != r2.Recovered || r1.Unrecoverable != r2.Unrecoverable ||
		len(r1.Ordered) != len(r2.Ordered) {
		t.Fatal("recovery runs not deterministic")
	}
	for i := range r1.Ordered {
		if r1.Ordered[i].SequenceNumber != r2.Ordered[i].SequenceNumber {
			t.Fatal("ordering differs across runs")
		}
	}
}

func TestPolicyDependencyOrder(t *testing.T) {
	items := []Recoverable{
		{TrackNumber: 1, TimeNs: 100, Offset: 1},
		{TrackNumber: 2, TimeNs: 50, Offset: 2, DependsOn: []uint32{1}},
	}
	plan := BuildPolicyOrder(items)
	if len(plan.Skipped) != 0 {
		t.Fatalf("unexpected skipped: %v", plan.Skipped)
	}
	if plan.Order[0].TrackNumber != 1 || plan.Order[1].TrackNumber != 2 {
		t.Fatalf("dependency must run first, got %v", plan.Order)
	}
}

func TestPolicyUnsatisfiedDependencySkipped(t *testing.T) {
	items := []Recoverable{
		{TrackNumber: 2, TimeNs: 50, DependsOn: []uint32{9}},
	}
	plan := BuildPolicyOrder(items)
	if len(plan.Order) != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("unsatisfied dependency must be skipped, got order=%d skipped=%d",
			len(plan.Order), len(plan.Skipped))
	}
}

func TestHighRecoveryRateWithinWindow(t *testing.T) {
	// 0..19, lose 1 packet (seq 7), rest present incl. late arrival -> high rate.
	var seq []uint16
	for s := uint16(0); s < 20; s++ {
		if s == 7 {
			continue
		}
		seq = append(seq, s)
	}
	pkts := make([]*rtp.Packet, len(seq))
	for i, s := range seq {
		pkts[i] = pkt(s)
	}
	res, _ := RecoverRTP(pkts, 8)
	if res.RecoveryRate() < 0.90 {
		t.Fatalf("in-window recovery rate %f below 0.90", res.RecoveryRate())
	}
}
