package pipeline

import (
	"testing"

	"mediacore/media"
)

func ptsFunc(baseNanos int64, stepNanos int64) func(seq int64) int64 {
	return func(seq int64) int64 { return baseNanos + seq*stepNanos }
}

func TestAudioSilenceFillUpToThree(t *testing.T) {
	f := NewAudioFiller(1, 48000, 480) // 10 ms packets
	events := f.FillHole(10, 13, ptsFunc(0, 10_000_000))
	if len(events) != 3 {
		t.Fatalf("events=%d want 3", len(events))
	}
	for _, e := range events {
		if e.Kind != RecSilenceFill || e.Samples != 480 {
			t.Fatalf("wrong fill event %+v", e)
		}
	}
	filled, unrec := f.Counters()
	if filled != 3 || unrec != 0 {
		t.Fatalf("filled=%d unrec=%d", filled, unrec)
	}
	// silence events must occupy contiguous, non-overlapping intervals
	for i := 1; i < len(events); i++ {
		if events[i].When.Start != events[i-1].When.End {
			t.Fatal("silence intervals not contiguous")
		}
	}
}

func TestAudioBurstBeyondThreeIsUnrecovered(t *testing.T) {
	f := NewAudioFiller(1, 48000, 480)
	events := f.FillHole(0, 5, ptsFunc(0, 10_000_000))
	var fills, unrec int
	for _, e := range events {
		switch e.Kind {
		case RecSilenceFill:
			fills++
		case RecUnrecoveredAudio:
			unrec++
		}
	}
	if fills != MaxAudioBurst || unrec != 2 {
		t.Fatalf("fills=%d unrec=%d", fills, unrec)
	}
	filled, unrecCount := f.Counters()
	if filled != 3 || unrecCount != 2 {
		t.Fatalf("counters filled=%d unrec=%d", filled, unrecCount)
	}
}

func TestVideoGapWaitsForKeyframeNoFakeFrames(t *testing.T) {
	v := NewVideoRecovery(2)
	hole := v.OpenHole(7, 9, media.Interval{Start: 100, End: 200})
	if hole.Kind != RecVideoGap || !v.WaitingKey() {
		t.Fatal("video hole not opened")
	}
	// non-key frames while waiting must be suppressed, never fabricated
	ok, evs := v.AcceptFrame(false, 9, media.Interval{Start: 200, End: 300})
	if ok || evs[0].Kind != RecFrameSuppressed {
		t.Fatal("non-key frame accepted during open hole")
	}
	ok, _ = v.AcceptFrame(false, 10, media.Interval{Start: 300, End: 400})
	if ok {
		t.Fatal("second non-key frame should also be suppressed")
	}
	// key frame resyncs exactly once
	ok, evs = v.AcceptFrame(true, 11, media.Interval{Start: 400, End: 500})
	if !ok || evs[0].Kind != RecVideoResync || v.WaitingKey() {
		t.Fatal("key frame did not resync")
	}
	// after resync normal frames flow again
	if ok, _ := v.AcceptFrame(false, 12, media.Interval{Start: 500, End: 600}); !ok {
		t.Fatal("post-resync frame rejected")
	}
	gaps, suppressed, resyncs := v.Counters()
	if gaps != 1 || suppressed != 2 || resyncs != 1 {
		t.Fatalf("gaps=%d suppressed=%d resyncs=%d", gaps, suppressed, resyncs)
	}
}

func TestRecoveryCheckpointRoundTrip(t *testing.T) {
	f := NewAudioFiller(1, 48000, 480)
	v := NewVideoRecovery(2)
	f.FillHole(0, 4, ptsFunc(0, 10_000_000)) // 3 fills + 1 unrecovered
	v.OpenHole(1, 2, media.Interval{Start: 0, End: 10})
	v.AcceptFrame(false, 2, media.Interval{Start: 10, End: 20})

	snap := f.Snapshot(v)
	f2, v2 := RestoreRecovery(1, 48000, 480, snap)
	filled, unrec := f2.Counters()
	if filled != 3 || unrec != 1 {
		t.Fatalf("audio counters after restore: %d/%d", filled, unrec)
	}
	gaps, suppressed, _ := v2.Counters()
	if gaps != 1 || suppressed != 1 || !v2.WaitingKey() {
		t.Fatalf("video state after restore: gaps=%d suppressed=%d waiting=%v",
			gaps, suppressed, v2.WaitingKey())
	}
}
