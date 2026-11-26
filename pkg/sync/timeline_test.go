package sync_test

import (
	"testing"

	"github.com/zld/medcheck/pkg/media"
	"github.com/zld/medcheck/pkg/sync"
)

func TestRationalConversionAndTimelineAlignmentDrift(t *testing.T) {
	tl := sync.NewTimeline()
	if err := tl.AddClock(sync.TrackClock{TrackID: "video", TimeBase: media.Clock90k}); err != nil {
		t.Fatal(err)
	}
	if err := tl.AddClock(sync.TrackClock{
		TrackID: "audio", TimeBase: media.Rational{Num: 1, Den: 48000}, OffsetUS: 500,
	}); err != nil {
		t.Fatal(err)
	}

	var video, audio []sync.Point
	for i := 0; i < 12; i++ {
		v, err := tl.Map("video", uint32(i*3000), int64(i), int64(i*100))
		if err != nil {
			t.Fatal(err)
		}
		video = append(video, v)

		// 1600 samples/frame at 48 kHz plus a small cumulative clock drift.
		audioTicks := int64(i*1600 + i*5)
		a, err := tl.Map("audio", uint32(audioTicks), int64(100+i), int64(i*90))
		if err != nil {
			t.Fatal(err)
		}
		audio = append(audio, a)
	}

	al, err := sync.Align("video", "audio", video, audio)
	if err != nil {
		t.Fatal(err)
	}
	if al.AVOffsetUS != 500 {
		t.Fatalf("offset=%d want 500", al.AVOffsetUS)
	}
	if al.DriftUS > 2000 || al.MaxAVDiffUS > 2000 {
		t.Fatalf("drift=%dus maxDiff=%dus exceed 2000", al.DriftUS, al.MaxAVDiffUS)
	}
}

func TestTimestampWrapAndAnomalyRecordedWithCoordinates(t *testing.T) {
	tl := sync.NewTimeline()
	if err := tl.AddClock(sync.TrackClock{TrackID: "cam9/audio", TimeBase: media.Rational{Num: 1, Den: 48000}}); err != nil {
		t.Fatal(err)
	}
	last := int64(0)
	for _, ts := range []uint32{0xfffffff0, 0xfffffffe, 1, 3} {
		p, err := tl.Map("cam9/audio", ts, last, int64(512+last*10))
		if err != nil {
			t.Fatal(err)
		}
		last = p.ExtTS
	}
	if last < 1<<32 {
		t.Fatalf("timestamp did not unwrap across 32-bit boundary: %d", last)
	}

	_, err := tl.Map("cam9/audio", 2, 77, 9001) // small backward move after ts=3
	if err != nil {
		t.Fatal(err)
	}
	var anomaly *media.Error
	for _, e := range tl.Issues {
		if e.Category == media.CatTimestampAnomaly {
			anomaly = e
		}
	}
	if anomaly == nil {
		t.Fatalf("expected timestamp anomaly, issues=%+v", tl.Issues)
	}
	if anomaly.TrackID != "cam9/audio" || anomaly.Sequence != 77 || anomaly.Offset != 9001 {
		t.Fatalf("anomaly coordinates wrong: %+v", anomaly)
	}
}
