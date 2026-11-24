package sync

import (
	"errors"
	"testing"

	"mediapipeline/internal/clock"
	mpierr "mediapipeline/internal/errors"
)

func points(audioStart, videoStart uint64) (TrackPoint, TrackPoint) {
	return TrackPoint{Kind: clock.KindAudio, Start: audioStart, Duration: 960},
		TrackPoint{Kind: clock.KindVideo, Start: videoStart, Duration: 960}
}

func TestAlignWithinTolerance(t *testing.T) {
	a, err := New(Config{ToleranceTicks: 480, Node: "sync"}, clock.NewTimeline(48000))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := a.Align(points(100, 400))
	if err != nil || !rep.WithinTolerance || rep.AbsOffsetTicks != 300 || rep.OffsetTicks != -300 {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
}

func TestAlignBeyondToleranceBlocks(t *testing.T) {
	a, _ := New(Config{ToleranceTicks: 240, Node: "sync"}, clock.NewTimeline(48000))
	rep, err := a.Align(points(1000, 0))
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatSync {
		t.Fatalf("err = %v, want CatSync", err)
	}
	if rep.WithinTolerance || rep.AbsOffsetTicks != 1000 {
		t.Fatalf("out-of-tolerance report wrong: %+v", rep)
	}
}

func TestRetryDoesNotChangeIntervals(t *testing.T) {
	tl := clock.NewTimeline(48000)
	a, _ := New(Config{ToleranceTicks: 100, Node: "sync"}, tl)
	av, vv := points(0, 50)
	first, err := a.Align(av, vv)
	if err != nil {
		t.Fatal(err)
	}
	// Re-running the same alignment must yield an identical result.
	second, err := a.Align(av, vv)
	if err != nil || first != second {
		t.Fatalf("retry changed alignment: %+v vs %+v", first, second)
	}
}

func TestRejectsWrongKinds(t *testing.T) {
	a, _ := New(Config{ToleranceTicks: 10}, clock.NewTimeline(48000))
	bad := TrackPoint{Kind: clock.KindVideo}
	if _, err := a.Align(bad, bad); err == nil {
		t.Fatal("two video points must be rejected")
	}
}

func TestOffsetMs(t *testing.T) {
	tl := clock.NewTimeline(48000)
	a, _ := New(Config{ToleranceTicks: 1 << 20, Node: "sync"}, tl)
	rep, _ := a.Align(points(48000, 0)) // 1 second audio lead
	if got := a.OffsetMs(rep); got != 1000 {
		t.Fatalf("offset ms = %d, want 1000", got)
	}
}

func TestNilTimelineRejected(t *testing.T) {
	if _, err := New(Config{}, nil); err == nil {
		t.Fatal("nil timeline must be rejected")
	}
}
