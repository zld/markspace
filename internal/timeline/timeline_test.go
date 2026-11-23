package timeline_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"medfix/internal/clock"
	"medfix/internal/timeline"
)

func hsh(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func ev(kind timeline.EventKind, track string, seq int, rawTS int64, payload string, off int64) timeline.Event {
	return timeline.Event{
		Kind: kind, TrackID: track, SourceID: "src", Seq: seq, RawTS: rawTS,
		RawPayload: []byte(payload), ContentHash: hsh(payload),
		Offset: timeline.OffsetRange{Begin: off, End: off + int64(len(payload))},
	}
}

func TestAlignTimescaleConversionExact(t *testing.T) {
	cfg := timeline.DefaultConfig()
	a := timeline.NewAligner(cfg)
	a.AddTrack(timeline.Track{ID: "v", Timescale: 90000, IsKeyframe: true})
	a.AddTrack(timeline.Track{ID: "au", Timescale: 48000})
	for i := 0; i < 10; i++ {
		_ = a.AddEvent(ev(timeline.KindSample, "v", i, int64(i)*3000, "v", int64(i)))
		_ = a.AddEvent(ev(timeline.KindSample, "au", 100+i, int64(i)*1024, "a", 1000+int64(i)))
	}
	res, err := a.Align()
	if err != nil {
		t.Fatalf("unexpected conflict: %v", err)
	}
	if len(res.Events) != 20 {
		t.Fatalf("events=%d want 20", len(res.Events))
	}
	// First event of each track anchors at tick 0; verify exact conversion
	// of the second video sample: 3000 @90k = 1/30 s.
	var v1 int64 = -1
	for _, e := range res.Events {
		if e.TrackID == "v" && e.Seq == 1 {
			v1 = e.Ticks
		}
	}
	if v1 != clock.ToTicks(3000, 90000) {
		t.Fatalf("video tick=%d want %d", v1, clock.ToTicks(3000, 90000))
	}
	// Global order must be non-decreasing in ticks.
	for i := 1; i < len(res.Events); i++ {
		if res.Events[i].Ticks < res.Events[i-1].Ticks {
			t.Fatalf("output not sorted at %d", i)
		}
	}
}

func TestAlignDedupByOffsetAndHash(t *testing.T) {
	a := timeline.NewAligner(timeline.DefaultConfig())
	a.AddTrack(timeline.Track{ID: "v", Timescale: 1000, IsKeyframe: true})
	_ = a.AddEvent(ev(timeline.KindSample, "v", 0, 0, "same", 10))
	_ = a.AddEvent(ev(timeline.KindSample, "v", 1, 10, "same", 10)) // duplicate
	_ = a.AddEvent(ev(timeline.KindSample, "v", 2, 20, "different", 20))
	res, err := a.Align()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 2 {
		t.Fatalf("dedup failed: %d events remain", len(res.Events))
	}
	// Same offset but different content must NOT be deduped.
	a2 := timeline.NewAligner(timeline.DefaultConfig())
	a2.AddTrack(timeline.Track{ID: "v", Timescale: 1000, IsKeyframe: true})
	_ = a2.AddEvent(ev(timeline.KindSample, "v", 0, 0, "x", 10))
	_ = a2.AddEvent(ev(timeline.KindSample, "v", 1, 10, "y", 10))
	res2, _ := a2.Align()
	if len(res2.Events) != 2 {
		t.Fatal("same offset with different content must be kept")
	}
}

func TestAlignCrossTrackConflictBlocks(t *testing.T) {
	cfg := timeline.DefaultConfig()
	cfg.ToleranceTicks = 1_000_000 // 1 ms
	a := timeline.NewAligner(cfg)
	a.AddTrack(timeline.Track{ID: "v", Timescale: 1000, IsKeyframe: true})
	a.AddTrack(timeline.Track{ID: "s", Timescale: 1000})
	_ = a.AddEvent(ev(timeline.KindSample, "v", 0, 0, "key", 0))
	// Same subtitle payload on both tracks, 100 ms apart > 1 ms tolerance.
	e1 := ev(timeline.KindSubtitle, "v", 1, 100, "hello", 10)
	e2 := ev(timeline.KindSubtitle, "s", 2, 200, "hello", 20)
	_ = a.AddEvent(e1)
	_ = a.AddEvent(e2)
	res, err := a.Align()
	if !errors.Is(err, timeline.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if len(res.Conflicts) == 0 || res.Conflicts[0].Code != timeline.CodeTimescaleConflict {
		t.Fatalf("conflict not recorded: %+v", res.Conflicts)
	}
	// The fault must still locate a raw source offset; deterministic order
	// (track ID then offset) selects track "s" first.
	if res.Conflicts[0].Offset != 20 || res.Conflicts[0].TrackID != "s" {
		t.Fatalf("conflict offset=%d track=%s", res.Conflicts[0].Offset, res.Conflicts[0].TrackID)
	}
	// Both offending offsets must appear somewhere in the conflict list.
	offSet := map[int64]bool{}
	for _, c := range res.Conflicts {
		offSet[c.Offset] = true
	}
	if !offSet[10] || !offSet[20] {
		t.Fatalf("both source offsets must be reported: %v", offSet)
	}
	var marked int
	for _, e := range res.Events {
		if e.Conflict {
			marked++
		}
	}
	if marked != 2 {
		t.Fatalf("marked events=%d want 2", marked)
	}
}

func TestAlignNoKeyframeTrackStatus(t *testing.T) {
	a := timeline.NewAligner(timeline.DefaultConfig())
	a.AddTrack(timeline.Track{ID: "meta", Timescale: 1000})
	_ = a.AddEvent(ev(timeline.KindChapter, "meta", 0, 0, "ch1", 0))
	_ = a.AddEvent(ev(timeline.KindChapter, "meta", 1, 500, "ch2", 8))
	res, err := a.Align()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NoKeyframeTracks) != 1 || res.NoKeyframeTracks[0] != "meta" {
		t.Fatalf("no-keyframe status wrong: %v", res.NoKeyframeTracks)
	}
}

func TestAlignMonotonicAndRuleProvenance(t *testing.T) {
	cfg := timeline.DefaultConfig()
	cfg.DriftSlopeNum, cfg.DriftSlopeDen = 1001, 1000
	a := timeline.NewAligner(cfg)
	a.AddTrack(timeline.Track{ID: "v", Timescale: 90000, IsKeyframe: true})
	for i := 0; i < 8; i++ {
		e := ev(timeline.KindSample, "v", i, int64(i)*3000, "p", int64(i*10))
		e.Keyframe = i%4 == 0
		_ = a.AddEvent(e)
	}
	res, err := a.Align()
	if err != nil {
		t.Fatal(err)
	}
	var prev int64
	for i, e := range res.Events {
		if e.RuleID == "" {
			t.Fatalf("event %d missing repair rule", i)
		}
		if i > 0 && e.Ticks < prev {
			t.Fatal("repaired timeline not monotonic")
		}
		prev = e.Ticks
	}
	if len(res.Rules) == 0 || res.Rules[0].SlopeNum != 1001 {
		t.Fatalf("repair rule not recorded: %+v", res.Rules)
	}
}

func TestAlignEventForUnknownTrackRejected(t *testing.T) {
	a := timeline.NewAligner(timeline.DefaultConfig())
	err := a.AddEvent(ev(timeline.KindSample, "ghost", 0, 0, "x", 0))
	var te *timeline.Error
	if !errors.As(err, &te) || te.Stage != "transform" || te.Offset != 0 {
		t.Fatalf("want structured offset error, got %v", err)
	}
}
