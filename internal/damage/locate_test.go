package damage_test

import (
	"testing"

	"media-check/internal/damage"
	"media-check/internal/fault"
	"media-check/internal/fixture"
	"media-check/internal/rtcp"
	rtpbuf "media-check/internal/rtp"
	"media-check/internal/stream"
)

func runPipeline(t *testing.T, dmg fixture.DamageConfig) (*fault.Model, *stream.Report, *fixture.Stream) {
	t.Helper()
	fm := fault.NewModel()
	s, err := fixture.Build(17, fixture.DefaultVideoConfig(), fixture.DefaultAudioConfig(), dmg)
	if err != nil {
		t.Fatal(err)
	}
	mapper := rtcp.NewTimeMapper(fm)
	for _, r := range s.Reports {
		if err := mapper.AddReport(r); err != nil {
			t.Fatal(err)
		}
	}
	meta := map[uint32]stream.TrackMeta{}
	for _, c := range s.Tracks {
		meta[c.SSRC] = stream.TrackMeta{
			TrackID: c.ID, Kind: c.Kind, ClockRate: c.ClockRate, KeyEvery: c.KeyEvery,
		}
	}
	buffers := map[uint32]*rtpbuf.ReorderBuffer{}
	var ordered []rtpbuf.OrderedPacket
	for _, rp := range s.Packets {
		b, ok := buffers[rp.Packet.SSRC]
		if !ok {
			b = rtpbuf.NewReorderBuffer(rp.TrackID, rp.Packet.SSRC, rtpbuf.DefaultWindowSize, fm)
			buffers[rp.Packet.SSRC] = b
		}
		ordered = append(ordered, b.Push(rp.Packet)...)
	}
	for _, b := range buffers {
		ordered = append(ordered, b.Flush()...)
	}
	rep, err := stream.NewInspector(mapper, fm, meta).Inspect(ordered)
	if err != nil {
		t.Fatal(err)
	}
	return fm, rep, s
}

func TestSegmentsCarryTimeAndOffset(t *testing.T) {
	video := fixture.DefaultVideoConfig()
	fm, rep, _ := runPipeline(t, fixture.DamageConfig{
		CorruptUnits:  map[uint32][]int{video.ID: {10}},
		TruncateUnits: map[uint32][]int{video.ID: {11}},
	})
	segs := damage.NewLocator(fm).Locate(rep)
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1 merged segment: %+v", len(segs), segs)
	}
	s := segs[0]
	if s.FirstErrOffset != fixture.MediaHeaderLen {
		t.Fatalf("first error offset = %d, want %d", s.FirstErrOffset, fixture.MediaHeaderLen)
	}
	if s.End.Cmp(s.Start) < 0 {
		t.Fatalf("segment time range inverted: %s..%s", s.Start, s.End)
	}
	hasCorrupt, hasTrunc := false, false
	for _, c := range s.Codes {
		hasCorrupt = hasCorrupt || c == fault.CodeCorruptPayload
		hasTrunc = hasTrunc || c == fault.CodeTruncated
	}
	if !hasCorrupt || !hasTrunc {
		t.Fatalf("merged codes = %v, want both corrupt and truncated", s.Codes)
	}
}

func TestLossSegmentBoundedByNeighborSamples(t *testing.T) {
	video := fixture.DefaultVideoConfig()
	// A 3-unit loss in the middle: neighbors survive, so the segment must
	// span from the sample before the gap to the sample after it.
	fm, rep, _ := runPipeline(t, fixture.DamageConfig{
		LossUnits: map[uint32][]int{video.ID: {40, 41, 42}},
	})
	segs := damage.NewLocator(fm).Locate(rep)
	var loss *damage.Segment
	for i := range segs {
		for _, c := range segs[i].Codes {
			if c == fault.CodeLost {
				loss = &segs[i]
			}
		}
	}
	if loss == nil {
		t.Fatalf("no loss segment located: %+v", segs)
	}
	if loss.SeqEnd-loss.SeqStart != 2 {
		t.Fatalf("loss seq span = %d..%d, want 3 packets", loss.SeqStart, loss.SeqEnd)
	}
	if loss.Start.Cmp(loss.End) > 0 {
		t.Fatal("loss segment time bounds invalid")
	}
}

func TestRecoveryRateMatchesGroundTruth(t *testing.T) {
	video := fixture.DefaultVideoConfig()
	fm, rep, s := runPipeline(t, fixture.DamageConfig{
		LossUnits: map[uint32][]int{video.ID: {40, 41, 42}},
	})
	recoverableGT := len(s.RecoverableUnits[video.ID])
	if recoverableGT != 3 {
		t.Fatalf("fixture recoverable units = %d, want 3", recoverableGT)
	}
	segs := damage.NewLocator(fm).Locate(rep)
	rr := damage.AggregateRecovery(segs)
	if rr.Rate < 0 || rr.Rate > 1 {
		t.Fatalf("rate out of [0,1]: %f", rr.Rate)
	}
	// All three gaps have surviving neighbors: every recoverable unit is
	// bounded by parsed media, so the rate is exactly 1.
	if rr.Recoverable != recoverableGT || rr.Rate != 1.0 {
		t.Fatalf("recovery = %+v, want %d recoverable at rate 1.0", rr, recoverableGT)
	}
	// Zero recoverable units must yield a defined 0, not NaN.
	if zero := damage.ComputeRecoveryRate(0, 0); zero.Rate != 0 {
		t.Fatalf("empty recovery = %f, want 0", zero.Rate)
	}
	// Over-recovery is clamped.
	if clamped := damage.ComputeRecoveryRate(5, 2); clamped.Rate != 1.0 {
		t.Fatalf("clamped rate = %f, want 1.0", clamped.Rate)
	}
}

func TestSameSeedProducesIdenticalSummary(t *testing.T) {
	video := fixture.DefaultVideoConfig()
	dmg := fixture.DamageConfig{
		LossUnits:    map[uint32][]int{video.ID: {40, 41}},
		CorruptUnits: map[uint32][]int{video.ID: {55}},
	}
	// Two independent runs with the same seed/config must be identical.
	fm1, rep1, _ := runPipeline(t, dmg)
	fm2, rep2, _ := runPipeline(t, dmg)
	sum1 := damage.Summarize(damage.NewLocator(fm1).Locate(rep1))
	sum2 := damage.Summarize(damage.NewLocator(fm2).Locate(rep2))
	if len(sum1) != len(sum2) {
		t.Fatalf("summary length drift: %d vs %d", len(sum1), len(sum2))
	}
	for i := range sum1 {
		if sum1[i] != sum2[i] {
			t.Fatalf("summary mismatch at %d:\n%+v\n%+v", i, sum1[i], sum2[i])
		}
	}
}
