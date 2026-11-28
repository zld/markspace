package fanin

import (
	"bytes"
	"testing"

	"mediacheck/internal/demux"
	merr "mediacheck/internal/errors"
	"mediacheck/internal/fanout"
	"mediacheck/internal/testmedia"
)

func buildFixture(t *testing.T, seed uint64) (*demux.Container, []fanout.Segment, Tolerances) {
	t.Helper()
	media, err := testmedia.BuildMP4(testmedia.DefaultSpec(seed))
	if err != nil {
		t.Fatal(err)
	}
	c, err := demux.Parse(bytes.NewReader(media.Container))
	if err != nil {
		t.Fatal(err)
	}
	segs := fanout.SegmentsFromTrack(c.Tracks[0])
	tol, err := TolerancesFromTracks(c.Tracks, c.MovieTimescale)
	if err != nil {
		t.Fatal(err)
	}
	return c, segs, tol
}

func alignedLayers(t *testing.T, segs []fanout.Segment) []*fanout.LayerState {
	t.Helper()
	ladder, err := fanout.NewLadder(90000, fanout.DefaultLayers(7))
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range segs {
		ladder.MarkSegment(seg)
	}
	return ladder.Layers()
}

func TestAlignedBoundariesPass(t *testing.T) {
	_, segs, tol := buildFixture(t, 7)
	rep, err := Verify(alignedLayers(t, segs), segs, tol.Video)
	if err != nil || !rep.OK {
		t.Fatalf("aligned segments must pass: %v %+v", err, rep)
	}
	if rep.StartError != 0 || rep.EndError != 0 {
		t.Fatalf("errors should be zero for exact alignment, got %d/%d",
			rep.StartError, rep.EndError)
	}
}

func TestBoundaryWithinOneFrameAccepted(t *testing.T) {
	_, segs, tol := buildFixture(t, 7)
	ladder, _ := fanout.NewLadder(90000, fanout.DefaultLayers(7))
	// Shift every edge by exactly one video frame; delta == limit is allowed.
	for _, seg := range segs {
		ladder.MarkSegment(ShiftSegment(seg, tol.Video.Limit))
	}
	rep, err := Verify(ladder.Layers(), segs, tol.Video)
	if err != nil || !rep.OK {
		t.Fatalf("one-frame shift must be tolerated, got %v %+v", err, rep)
	}
}

func TestBoundaryBeyondToleranceRejected(t *testing.T) {
	_, segs, tol := buildFixture(t, 7)
	ladder, _ := fanout.NewLadder(90000, fanout.DefaultLayers(7))
	for i, seg := range segs {
		if i == 1 {
			ladder.MarkSegment(ShiftSegment(seg, 2*tol.Video.Limit))
			continue
		}
		ladder.MarkSegment(seg)
	}
	_, err := Verify(ladder.Layers(), segs, tol.Video)
	e, ok := merr.As(err)
	if !ok || e.Code != merr.CodeBoundaryMismatch {
		t.Fatalf("got %v; want FANIN_BOUNDARY_MISMATCH", err)
	}
	if e.RawTimescale == 0 || e.RawTimestamp == 0 {
		t.Fatal("boundary error must carry the original timestamp")
	}
}

func TestDifferentTimescaleConvertedExactly(t *testing.T) {
	_, segs, tol := buildFixture(t, 7)
	// A layer on a 48kHz clock: 90k frame edges convert exactly to 48k.
	ladder, _ := fanout.NewLadder(48000, fanout.DefaultLayers(7)[:1])
	for _, seg := range segs {
		converted := seg
		converted.Timescale = 48000
		converted.StartTick = int64(float64(seg.StartTick) * 48000 / 90000)
		converted.EndTick = int64(float64(seg.EndTick) * 48000 / 90000)
		ladder.MarkSegment(converted)
	}
	rep, err := Verify(ladder.Layers(), segs, tol.Video)
	if err != nil || !rep.OK {
		t.Fatalf("cross-timescale aligned edges must pass: %v %+v", err, rep)
	}
}

func TestSegmentCountMismatchRejected(t *testing.T) {
	_, segs, tol := buildFixture(t, 7)
	ladder, _ := fanout.NewLadder(90000, fanout.DefaultLayers(7)[:1])
	ladder.MarkSegment(segs[0])
	if _, err := Verify(ladder.Layers(), segs, tol.Video); err == nil {
		t.Fatal("fewer layer segments than reference must fail")
	}
}

func TestAudioToleranceIsOnePacket(t *testing.T) {
	_, _, tol := buildFixture(t, 7)
	// 1024 audio ticks at 48kHz converts to 1920 ticks on the 90kHz clock.
	if tol.Audio.Limit != 1920 || tol.Video.Limit != 3600 {
		t.Fatalf("tolerances wrong: video=%d audio=%d", tol.Video.Limit, tol.Audio.Limit)
	}
}
