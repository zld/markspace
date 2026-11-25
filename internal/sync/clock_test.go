package sync

import (
	"testing"

	"mediasync/internal/model"
	"mediasync/internal/rtp"
)

func dseg(rep string, kind model.AssetKind, period, index int, start, dur int64, ts uint32) model.SegmentDescriptor {
	return model.SegmentDescriptor{
		Source: "dash", Representation: rep, Kind: kind, PeriodID: period, Index: index,
		Keyframe: true, StartNS: start, DurationNS: dur, Timescale: ts,
		StartInScale:  start * int64(ts) / 1_000_000_000,
		DurationScale: dur * int64(ts) / 1_000_000_000,
	}
}

func TestClockLaysPeriodsOutContiguously(t *testing.T) {
	clock := NewClock()
	// Period 0 dialogue: two 1ms segments at timescale 1000.
	p0 := []model.SegmentDescriptor{
		dseg("dialogue", model.KindDialogue, 0, 0, 0, 1_000_000, 1000),
		dseg("dialogue", model.KindDialogue, 0, 1, 1_000_000, 1_000_000, 1000),
	}
	aligned0 := clock.FanIn(p0)
	if aligned0[1].StartNS != 1_000_000 {
		t.Fatalf("period 0 timing wrong: %+v", aligned0[1])
	}

	// Period 1 dialogue restarts locally at zero; on the clock it must begin
	// exactly where period 0 ended (2ms), proving no cross-period stitching.
	p1 := []model.SegmentDescriptor{
		dseg("dialogue", model.KindDialogue, 1, 0, 0, 1_500_000, 1000),
	}
	aligned1 := clock.FanIn(p1)
	if aligned1[0].StartNS != 2_000_000 {
		t.Fatalf("period 1 must start at 2ms clock, got %d", aligned1[0].StartNS)
	}

	cuts := clock.Cuts()
	if len(cuts) != 1 || cuts[0].PeriodID != 1 || cuts[0].Reason != model.KindDiscontinuity {
		t.Fatalf("expected one discontinuity cut: %+v", cuts)
	}
}

func TestSubtitlesRealignToDialogueTimeline(t *testing.T) {
	clock := NewClock()
	// Dialogue first establishes period 0 spanning 3ms.
	clock.FanIn([]model.SegmentDescriptor{
		dseg("dialogue", model.KindDialogue, 0, 0, 0, 3_000_000, 1000),
	})
	// Period 1 dialogue establishes a new anchor starting at 3ms clock.
	clock.FanIn([]model.SegmentDescriptor{
		dseg("dialogue", model.KindDialogue, 1, 0, 0, 2_000_000, 1000),
	})
	// Subtitle in period 1 starts locally at 1ms; must land at 3ms+1ms clock.
	subs := clock.FanIn([]model.SegmentDescriptor{
		dseg("subs", model.KindSubtitle, 1, 0, 1_000_000, 500_000, 1000),
	})
	if subs[0].StartNS != 4_000_000 {
		t.Fatalf("subtitle must realign to 4ms clock, got %d", subs[0].StartNS)
	}
	cuts := clock.Cuts()
	found := false
	for _, cut := range cuts {
		for _, a := range cut.Alignments {
			if a.Representation == "subs" && a.ShiftNS == 3_000_000 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("subtitle shift not recorded: %+v", cuts)
	}
}

func TestFanoutLaneIsolationAndSuspension(t *testing.T) {
	f := NewFanout()
	f.AddLane("video-hi", model.KindVideo, model.PriorityHigh,
		rtp.NewJitterBuffer("video-hi", 8, 8), rtp.NewRecoverer("video-hi", 8, nil))
	f.AddLane("dialogue", model.KindDialogue, model.PriorityCore,
		rtp.NewJitterBuffer("dialogue", 8, 8), rtp.NewRecoverer("dialogue", 8, nil))
	f.AddLane("video-lo", model.KindVideo, model.PriorityLow,
		rtp.NewJitterBuffer("video-lo", 8, 8), rtp.NewRecoverer("video-lo", 8, nil))

	if !f.Dispatch(dseg("video-hi", model.KindVideo, 0, 0, 0, 1_000_000, 1000)) {
		t.Fatal("active lane must accept dispatch")
	}
	// Core lane cannot be suspended; low-priority lane can.
	if f.Suspend("dialogue") {
		t.Fatal("core dialogue lane must never suspend")
	}
	if !f.Suspend("video-lo") {
		t.Fatal("low-priority lane should suspend")
	}
	if f.Dispatch(dseg("video-lo", model.KindVideo, 0, 0, 0, 1_000_000, 1000)) {
		t.Fatal("suspended lane must reject dispatch")
	}

	clock := NewClock()
	merged := f.MergeInto(clock)
	if len(merged) != 1 || merged[0].Representation != "video-hi" {
		t.Fatalf("only active lanes should fan in, got %+v", merged)
	}
	if len(f.Lanes()) != 3 {
		t.Fatal("suspension must not deregister the lane")
	}
}

func TestFanInDeterministicOrder(t *testing.T) {
	clock1, clock2 := NewClock(), NewClock()
	lane := []model.SegmentDescriptor{
		dseg("sfx", model.KindSFX, 0, 2, 2_000_000, 1_000_000, 1000),
		dseg("sfx", model.KindSFX, 0, 0, 0, 1_000_000, 1000),
		dseg("sfx", model.KindSFX, 0, 1, 1_000_000, 1_000_000, 1000),
	}
	a := clock1.FanIn(lane)
	b := clock2.FanIn(lane)
	for i := range a {
		if a[i].StartNS != b[i].StartNS || a[i].Index != b[i].Index {
			t.Fatalf("fan-in ordering not deterministic at %d", i)
		}
	}
	if a[0].Index != 0 || a[1].Index != 1 || a[2].Index != 2 {
		t.Fatalf("segments not sorted by clock start: %+v", a)
	}
}
