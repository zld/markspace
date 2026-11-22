package ingest

import (
	"testing"

	"mediacore/media"
)

func mkPacket(seq uint16, ts uint32, size int) *Packet {
	return &Packet{StreamID: 7, Seq: seq, Timestamp: ts, Payload: make([]byte, size)}
}

func seqs(ps []*Packet) []uint16 {
	out := make([]uint16, len(ps))
	for i, p := range ps {
		out[i] = p.Seq
	}
	return out
}

func equalSeqs(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReorderOrdersWithinWindow(t *testing.T) {
	r := NewReorder(ReorderConfig{Window: 8})
	for _, seq := range []uint16{0, 1, 3, 2, 5, 4, 7, 6} {
		r.Add(mkPacket(seq, uint32(seq)*3000, 100))
	}
	res, err := r.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	want := []uint16{0, 1, 2, 3, 4, 5, 6, 7}
	if s := seqs(res.Packets); !equalSeqs(s, want) {
		t.Fatalf("got %v want %v", s, want)
	}
	if len(res.Gaps) != 0 {
		t.Fatalf("unexpected gaps: %+v", res.Gaps)
	}
}

func TestReorderSuppressesDuplicates(t *testing.T) {
	r := NewReorder(ReorderConfig{Window: 8})
	for _, seq := range []uint16{0, 1, 1, 2, 0, 3} {
		r.Add(mkPacket(seq, uint32(seq)*3000, 100))
	}
	res, _ := r.Finalize()
	if res.Duplicates != 2 {
		t.Fatalf("duplicates=%d want 2", res.Duplicates)
	}
	if got := seqs(res.Packets); !equalSeqs(got, []uint16{0, 1, 2, 3}) {
		t.Fatalf("packets=%v", got)
	}
}

func TestReorderEmitsGapForBurstLoss(t *testing.T) {
	r := NewReorder(ReorderConfig{Window: 4})
	for _, seq := range []uint16{0, 1, 5, 6, 7, 8, 9} {
		r.Add(mkPacket(seq, uint32(seq)*3000, 100))
	}
	res, _ := r.Finalize()
	if got := seqs(res.Packets); !equalSeqs(got, []uint16{0, 1, 5, 6, 7, 8, 9}) {
		t.Fatalf("packets=%v", got)
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("gaps=%+v", res.Gaps)
	}
	g := res.Gaps[0]
	if g.FromSeq != 2 || g.ToSeq != 4 || g.Count != 3 {
		t.Fatalf("gap=%+v want seq 2..4 count 3", g)
	}
}

func TestReorderLateAfterWindow(t *testing.T) {
	r := NewReorder(ReorderConfig{Window: 3})
	for _, seq := range []uint16{0, 1, 2, 6, 7} { // 3,4,5 missing -> gap declared
		r.Add(mkPacket(seq, 0, 10))
	}
	// 4 arrives after its gap was declared: late, not a duplicate.
	r.Add(mkPacket(4, 0, 10))
	res, _ := r.Finalize()
	if res.Late != 1 {
		t.Fatalf("late=%d want 1", res.Late)
	}
	if res.Duplicates != 0 {
		t.Fatalf("duplicates=%d want 0", res.Duplicates)
	}
}

func TestReorderSequenceWraparound(t *testing.T) {
	r := NewReorder(ReorderConfig{Window: 8})
	start := uint16(0xFFFB)
	for i := uint16(0); i < 10; i++ {
		r.Add(mkPacket(start+i, 0, 10))
	}
	res, err := r.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Packets) != 10 {
		t.Fatalf("packets=%d want 10 (wrap)", len(res.Packets))
	}
	want := []uint16{0xFFFB, 0xFFFC, 0xFFFD, 0xFFFE, 0xFFFF, 0, 1, 2, 3, 4}
	if got := seqs(res.Packets); !equalSeqs(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if len(res.Gaps) != 0 {
		t.Fatalf("unexpected gaps %+v", res.Gaps)
	}
}

func TestReorderFinalPartialGap(t *testing.T) {
	r := NewReorder(ReorderConfig{Window: 100})
	r.Add(mkPacket(0, 0, 10))
	r.Add(mkPacket(4, 0, 10))
	res, _ := r.Finalize()
	if len(res.Gaps) != 1 || res.Gaps[0].Count != 3 {
		t.Fatalf("gaps=%+v want one gap of 3", res.Gaps)
	}
	if got := seqs(res.Packets); !equalSeqs(got, []uint16{0, 4}) {
		t.Fatalf("packets=%v", got)
	}
}

func TestPacketRoundTrip(t *testing.T) {
	orig := &Packet{StreamID: 42, Seq: 1234, Timestamp: 90000, Marker: true, SSRC: 99,
		Payload: []byte{1, 2, 3, 4, 5}}
	raw, err := EncodePacket(orig)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePacket(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != orig.Seq || got.Timestamp != orig.Timestamp || got.StreamID != orig.StreamID ||
		got.Marker != orig.Marker || got.SSRC != orig.SSRC || len(got.Payload) != len(orig.Payload) {
		t.Fatalf("round trip diverged: %+v vs %+v", got, orig)
	}
}

func TestPacketTruncationReportsOffset(t *testing.T) {
	orig := &Packet{StreamID: 1, Seq: 1, Timestamp: 1, Payload: make([]byte, 10)}
	raw, _ := EncodePacket(orig)
	_, err := ParsePacket(raw[:len(raw)-2], 77)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	e, ok := media.AsError(err)
	if !ok || e.FrameOffset != 77 {
		t.Fatalf("error=%v", err)
	}
}

func TestBandwidthEstimateAndTarget(t *testing.T) {
	e := NewBandwidthEstimator(BandwidthConfig{WindowSamples: 4, DropRatio: 0.5})
	// 100 ms intervals carrying 11250 bytes each => 900 kbps.
	for i := 0; i <= 4; i++ {
		e.Observe(int64(i*100), 11250)
	}
	est := e.EstimateKBPS()
	if est < 899 || est > 901 {
		t.Fatalf("estimate=%.2f want ~900", est)
	}
	target, reason := e.TargetKBPS(e.EstimateKBPS(), e.InputCeilingKBPS())
	if target < 899 || target > 901 {
		t.Fatalf("target=%d reason=%s", target, reason)
	}
}

func TestBandwidthSuddenDropCapsTarget(t *testing.T) {
	e := NewBandwidthEstimator(BandwidthConfig{WindowSamples: 3, DropRatio: 0.5, InFlightCapMS: 200})
	for i := 0; i <= 3; i++ {
		e.Observe(int64(i*100), 11250) // 900 kbps
	}
	// Two low intervals push the window average well below half of peak.
	for i := 4; i <= 6; i++ {
		e.Observe(int64(i*100), 1125) // 90 kbps
	}
	if !e.DropDetected() {
		t.Fatalf("expected drop detection, est=%.2f peak=%.2f", e.EstimateKBPS(), e.peakKBPS)
	}
	target, reason := e.TargetKBPS(e.EstimateKBPS(), e.InputCeilingKBPS())
	if reason != "sudden_drop" && reason != "input_ceiling" {
		t.Fatalf("reason=%s target=%d", reason, target)
	}
	if int64(target) > int64(e.InputCeilingKBPS())+1 {
		t.Fatalf("target %d exceeds input ceiling %.2f", target, e.InputCeilingKBPS())
	}
	if lim := e.InFlightLimitBytes(e.EstimateKBPS()); lim <= 0 {
		t.Fatalf("inflight limit should be positive, got %d", lim)
	}
}

func TestBandwidthReportDeterministic(t *testing.T) {
	run := func() BandwidthReport {
		e := NewBandwidthEstimator(DefaultBandwidthConfig())
		for i := 0; i < 10; i++ {
			e.Observe(int64(i*40), 4500)
		}
		return e.Report()
	}
	if a, b := run(), run(); a != b {
		t.Fatalf("report not deterministic: %+v vs %+v", a, b)
	}
}

// TestReorderOutOfOrderStartKeepsDistinctSequences exercises the re-anchor path:
// the first arriving packet is ahead of a later, earlier packet, and the two
// must both be delivered once without aliasing.
func TestReorderOutOfOrderStartKeepsDistinctSequences(t *testing.T) {
	r := NewReorder(ReorderConfig{Window: 8})
	r.Add(mkPacket(10, 0, 10))
	r.Add(mkPacket(9, 0, 10))
	res, err := r.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(res.Packets); !equalSeqs(got, []uint16{9, 10}) {
		t.Fatalf("packets=%v want [9 10]", got)
	}
	if res.Duplicates != 0 || res.Late != 0 || len(res.Gaps) != 0 {
		t.Fatalf("unexpected dups=%d late=%d gaps=%+v", res.Duplicates, res.Late, res.Gaps)
	}
}

// TestReorderWrapAfterReorder ensures reference extension survives a wrap
// while packets are reordered.
func TestReorderWrapAfterReorder(t *testing.T) {
	r := NewReorder(ReorderConfig{Window: 8})
	start := uint16(0xFFFE)
	order := []uint16{0, 2, 1, 3, 5, 4, 6, 7}
	for _, i := range order {
		r.Add(mkPacket(start+i, uint32(int32(start)+int32(i)), 10))
	}
	res, err := r.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	want := []uint16{0xFFFE, 0xFFFF, 0, 1, 2, 3, 4, 5}
	if got := seqs(res.Packets); !equalSeqs(got, want) {
		t.Fatalf("packets=%v want %v", got, want)
	}
	if len(res.Gaps) != 0 {
		t.Fatalf("unexpected gaps %+v", res.Gaps)
	}
}
