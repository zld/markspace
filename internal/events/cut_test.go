package events_test

import (
	"testing"

	"replaycheck/internal/clock"
	"replaycheck/internal/events"
)

func vfrTimeline() clock.Timeline {
	deltas := []int64{1500, 4500, 3000, 1500, 4500, 3000}
	frames := make([]clock.Frame, 0, len(deltas))
	var raw int64
	for i, d := range deltas {
		startNS, _ := clock.Convert(raw, 90000)
		endNS, _ := clock.Convert(raw+d, 90000)
		frames = append(frames, clock.Frame{
			Index: i, DTSRaw: raw, DTSNS: startNS, DurationRaw: uint32(d), DurationNS: endNS - startNS,
		})
		raw += d
	}
	return clock.Timeline{TrackID: 7, Timebase: clock.Timebase{Timescale: 90000}, Frames: frames}
}

func TestMapEventUsesFollowingFrameAndQuantization(t *testing.T) {
	tl := vfrTimeline()
	start, _ := clock.Convert(5800, 90000)
	end, _ := clock.Convert(17800, 90000)
	cut, err := events.MapEvent(events.Event{ID: "e1", StartNS: start, EndNS: end}, tl)
	if err != nil {
		t.Fatal(err)
	}
	if cut.StartFrame != 2 {
		t.Fatalf("start frame = %d, want 2", cut.StartFrame)
	}
	if cut.StartQuantizationNS <= 0 || cut.StartQuantizationNS > tl.Frames[2].DurationNS {
		t.Fatalf("quantization %d outside frame duration %d", cut.StartQuantizationNS, tl.Frames[2].DurationNS)
	}
	wantQuant, _ := clock.Convert(200, 90000)
	if cut.StartQuantizationNS != wantQuant {
		t.Fatalf("start quant = %d, want %d", cut.StartQuantizationNS, wantQuant)
	}
	if cut.CutStartNS != tl.Frames[2].DTSNS {
		t.Fatalf("cut start = %d, want frame DTS %d", cut.CutStartNS, tl.Frames[2].DTSNS)
	}
}

func TestMapEventOutOfRange(t *testing.T) {
	tl := vfrTimeline()
	_, err := events.MapEvent(events.Event{ID: "bad", StartNS: 10 * clock.NanosecondsPerSecond, EndNS: 11 * clock.NanosecondsPerSecond}, tl)
	if err == nil {
		t.Fatal("expected out-of-range event error")
	}
}
