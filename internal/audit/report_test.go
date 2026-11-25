package audit

import (
	"testing"

	"mediacore/internal/model"
)

func TestCompareLossless(t *testing.T) {
	src := []model.Event{
		{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 0, Size: 3, Payload: []byte("abc")},
		{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 3000, Size: 3, Payload: []byte("def")},
		{TrackID: "mp4:2", Kind: model.EventSubtitle, Ticks: 1500, Size: 2, Payload: []byte("hi")},
	}
	out := []model.Event{
		{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 0, Size: 3, Payload: []byte("abc")},
		{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 3000, Size: 3, Payload: []byte("def")},
		{TrackID: "mp4:2", Kind: model.EventSubtitle, Ticks: 1501, Size: 2, Payload: []byte("hi")},
	}
	rep := Compare("s1", "k1", src, out, 10, nil)
	if !rep.OK || rep.ByteDeviation != 0 || rep.MaxTimeDevTicks != 1 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if rep.PayloadSHA256 == "" {
		t.Fatal("missing payload digest")
	}
}

func TestCompareDetectsLoss(t *testing.T) {
	src := []model.Event{
		{Kind: model.EventMedia, Ticks: 0, Size: 3, Payload: []byte("abc")},
		{Kind: model.EventMedia, Ticks: 1, Size: 5, Payload: []byte("lost!")},
	}
	out := []model.Event{
		{Kind: model.EventMedia, Ticks: 0, Size: 3, Payload: []byte("abc")},
	}
	rep := Compare("s", "k", src, out, 0, nil)
	if rep.OK || rep.ByteDeviation != 5 {
		t.Fatalf("want byte deviation 5: %+v", rep)
	}
}

func TestCompareDetectsExtraOutput(t *testing.T) {
	src := []model.Event{{Kind: model.EventMedia, Size: 1, Payload: []byte("a")}}
	out := []model.Event{
		{Kind: model.EventMedia, Size: 1, Payload: []byte("a")},
		{Kind: model.EventMedia, Size: 4, Payload: []byte("junk")},
	}
	rep := Compare("s", "k", src, out, 0, nil)
	if rep.OK || rep.ByteDeviation != 4 {
		t.Fatalf("want byte deviation 4: %+v", rep)
	}
}

func TestCompareToleranceFailure(t *testing.T) {
	ev := func(t int64) []model.Event {
		return []model.Event{{Kind: model.EventMedia, Ticks: t, Size: 1, Payload: []byte("a")}}
	}
	rep := Compare("s", "k", ev(0), ev(100), 50, nil)
	if rep.OK || rep.MaxTimeDevTicks != 100 {
		t.Fatalf("want tolerance failure: %+v", rep)
	}
}
