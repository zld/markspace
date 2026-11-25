package repair

import (
	"testing"

	"mediavault/internal/timeline"
)

func newSetup(t *testing.T, threshold uint64) (*BoundaryRepairer, *timeline.TrackNormalizer, timeline.TrackID) {
	id := timeline.TrackID{Source: "rtp", Index: 0, Kind: timeline.KindVideo}
	norm := timeline.NewTrackNormalizer(id, timeline.ClockRef{Source: "rtp", Clock: 7}, 90000, threshold)
	rep := NewBoundaryRepairer(1 << 33)
	rep.Register(id, norm)
	return rep, norm, id
}

func pushGood(t *testing.T, norm *timeline.TrackNormalizer, ts uint32, seq uint16) *timeline.Timestamp {
	got, err := norm.PushRTP(rtpPacket(ts, seq))
	if err != nil || got == nil {
		t.Fatalf("good packet ts=%d failed: %v", ts, err)
	}
	return got
}

func TestRepairRestoresMonotonicWithTrace(t *testing.T) {
	rep, norm, id := newSetup(t, 1000)
	good := pushGood(t, norm, 9000, 1)

	// Source switch resets the raw clock to 100.
	_, err := norm.PushRTP(rtpPacket(100, 2))
	if err == nil {
		t.Fatal("expected boundary-jump error")
	}
	se := err.(*timeline.StageError)
	se.Offset = 424
	if se.Kind != timeline.KindBoundaryJump || !se.Recoverable {
		t.Fatalf("want recoverable boundary jump, got %#v", se)
	}

	fixed, err := rep.Handle(se, 100)
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if fixed.DTS.Cmp(good.DTS) <= 0 {
		t.Fatal("corrected DTS must be strictly after the last good DTS")
	}
	corr := rep.Corrections()
	if len(corr) != 1 {
		t.Fatalf("corrections = %d, want 1", len(corr))
	}
	c := corr[0]
	if c.RawDTS != 100 || c.OffsetTicks == 0 || c.CorrectedDTS != 100+c.OffsetTicks {
		t.Fatalf("correction not traceable: %+v", c)
	}
	if c.ByteOffset != 424 || c.Track != id.String() || !c.Recovered {
		t.Fatalf("correction must retain offset/track/recovered: %+v", c)
	}
	// Subsequent packets on the new clock inherit the active offset and
	// stay monotonic. PushSample accepts the already-adapted tick value.
	adapted := rep.Adapt(id, 130)
	next, err := norm.PushSample(adapted, adapted)
	if err != nil || next.DTS.Cmp(fixed.DTS) <= 0 {
		t.Fatalf("post-repair packet not monotonic: %v", err)
	}
}

func TestRepairFailsUnrepairableTrack(t *testing.T) {
	rep, norm, _ := newSetup(t, 1000)
	pushGood(t, norm, 9000, 1)
	_, err := norm.PushRTP(rtpPacket(100, 2))
	se := err.(*timeline.StageError)

	// Tighten the bound so the same excursion is now unrecoverable.
	rep.MaxBackwardTicks = 10
	if _, err := rep.Handle(se, 100); err != timeline.ErrTrackFailed {
		t.Fatalf("want ErrTrackFailed, got %v", err)
	}
	if !norm.Failed() {
		t.Fatal("normalizer must be marked failed")
	}
	if len(rep.Failed()) != 1 || rep.Failed()[0].Kind != timeline.KindUnrecoverable {
		t.Fatalf("failed-track record missing: %+v", rep.Failed())
	}
	// A later push keeps erroring instead of silently dropping.
	if _, err := norm.PushRTP(rtpPacket(200, 3)); err == nil {
		t.Fatal("failed track must keep returning an error")
	}
}

func TestRepairDoesNotPolluteSiblingTrack(t *testing.T) {
	rep, norm, id := newSetup(t, 1000)
	otherID := timeline.TrackID{Source: "rtp", Index: 1, Kind: timeline.KindAudio}
	other := timeline.NewTrackNormalizer(otherID, timeline.ClockRef{Source: "rtp", Clock: 8}, 48000, 1000)
	rep.Register(otherID, other)

	pushGood(t, norm, 9000, 1)
	_, err := norm.PushRTP(rtpPacket(100, 2))
	se := err.(*timeline.StageError)
	if _, err := rep.Handle(se, 100); err != nil {
		t.Fatal(err)
	}
	// The sibling track keeps its own clean timeline.
	sib, err := other.PushRTP(rtpPacket(480, 1))
	if err != nil || sib == nil {
		t.Fatalf("sibling track must stay unaffected: %v", err)
	}
	if len(rep.Corrections()) != 1 || rep.Corrections()[0].Track != id.String() {
		t.Fatal("correction must be scoped to one track")
	}
}
