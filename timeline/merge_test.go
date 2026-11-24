package timeline_test

import (
	"testing"

	"mediacheck/timeline"
)

const (
	videoTrack = uint32(1)
	audioTrack = uint32(2)
)

func videoInput() timeline.TrackInput {
	return timeline.TrackInput{Number: videoTrack, UID: 101, IsVideo: true, FrameRate: 24}
}

func audioInput() timeline.TrackInput {
	return timeline.TrackInput{Number: audioTrack, UID: 202, SampleRate: 48000}
}

func nsAtFrame(f int) int64 {
	ns, _ := timeline.NsFromUnits(int64(f), 24)
	return ns
}

func TestMergeHealthyInterleaved(t *testing.T) {
	cfg := timeline.DefaultMergeConfig()
	tracks := []timeline.TrackInput{videoInput(), audioInput()}
	var blocks []timeline.BlockInput
	for f := 0; f < 6; f++ {
		blocks = append(blocks,
			timeline.BlockInput{TrackNumber: videoTrack, TimeNs: nsAtFrame(f), CheckOK: true},
			timeline.BlockInput{TrackNumber: audioTrack, TimeNs: nsAtFrame(f), CheckOK: true},
		)
	}
	res, err := timeline.Merge(blocks, tracks, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Trusted {
		t.Fatalf("healthy merge must be trusted: %s", res.RejectReason)
	}
	if len(res.Events) != 12 {
		t.Fatalf("want 12 merged events, got %d", len(res.Events))
	}
	// Global order is non-decreasing and ties put video (track 1) first.
	for i := 1; i < len(res.Events); i++ {
		a, b := res.Events[i-1], res.Events[i]
		if a.TimeNs > b.TimeNs || (a.TimeNs == b.TimeNs && a.TrackNumber > b.TrackNumber) {
			t.Fatalf("merge order broken at %d", i)
		}
	}
	if len(res.Tracks[videoTrack].Gaps) != 0 {
		t.Fatalf("unexpected gaps: %+v", res.Tracks[videoTrack].Gaps)
	}
}

func TestMergeMissingBlockSynthesised(t *testing.T) {
	cfg := timeline.DefaultMergeConfig()
	// Frames 0,1,3,4: frame 2 is missing.
	blocks := []timeline.BlockInput{
		{TrackNumber: videoTrack, TimeNs: nsAtFrame(0)},
		{TrackNumber: videoTrack, TimeNs: nsAtFrame(1)},
		{TrackNumber: videoTrack, TimeNs: nsAtFrame(3)},
		{TrackNumber: videoTrack, TimeNs: nsAtFrame(4)},
	}
	res, err := timeline.Merge(blocks, []timeline.TrackInput{videoInput()}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Trusted {
		t.Fatal("a bounded missing block still yields a trusted timeline")
	}
	tl := res.Tracks[videoTrack]
	if len(tl.Gaps) != 1 {
		t.Fatalf("want 1 gap, got %d", len(tl.Gaps))
	}
	missing := 0
	for _, ev := range tl.Events {
		if ev.Missing {
			missing++
			if ev.TimeNs != nsAtFrame(2) {
				t.Fatalf("placeholder at wrong time %d", ev.TimeNs)
			}
		}
	}
	if missing != 1 {
		t.Fatalf("want 1 missing placeholder, got %d", missing)
	}
}

func TestMergeEmptyTrackUntrusted(t *testing.T) {
	res, err := timeline.Merge(nil, []timeline.TrackInput{videoInput(), audioInput()}, timeline.DefaultMergeConfig())
	if err != nil {
		t.Fatal(err)
	}
	if res.Trusted {
		t.Fatal("declared track with no blocks must be untrusted")
	}
	if res.RejectReason == "" {
		t.Fatal("reject reason must be recorded")
	}
}

func TestMergeUniqueRolloverRecovered(t *testing.T) {
	// Simulate raw timecodes in ticks with a small rollover period so the
	// same logic that protects 32-bit wrap is exercised deterministically.
	cfg := timeline.MergeConfig{RolloverTicks: 100, MaxStepTicks: 8, MaxMissingIntervals: 64}
	spec := timeline.TrackInput{Number: videoTrack, UID: 1, IsVideo: true, FrameRate: 10}
	mk := func(tick int64) timeline.BlockInput {
		ns, _ := timeline.NsFromUnits(tick, 10)
		return timeline.BlockInput{TrackNumber: videoTrack, TimeNs: ns}
	}
	// Ticks 96,97,98 then wrap to 1,2 (=101,102 absolute).
	blocks := []timeline.BlockInput{mk(96), mk(97), mk(98), mk(1), mk(2)}
	res, err := timeline.Merge(blocks, []timeline.TrackInput{spec}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Trusted {
		t.Fatalf("single-period rollover must be recoverable: %s", res.RejectReason)
	}
	last := res.Tracks[videoTrack].Events[len(res.Tracks[videoTrack].Events)-1]
	wantNs, _ := timeline.NsFromUnits(102, 10)
	if last.TimeNs != wantNs || last.WrapCount != 1 {
		t.Fatalf("recovered last event wrong: %+v want ns=%d", last, wantNs)
	}
}

func TestMergeAmbiguousRolloverUntrusted(t *testing.T) {
	cfg := timeline.MergeConfig{RolloverTicks: 100, MaxStepTicks: 8, MaxMissingIntervals: 64}
	spec := timeline.TrackInput{Number: videoTrack, UID: 1, IsVideo: true, FrameRate: 10}
	mk := func(tick int64) timeline.BlockInput {
		ns, _ := timeline.NsFromUnits(tick, 10)
		return timeline.BlockInput{TrackNumber: videoTrack, TimeNs: ns}
	}
	// Backward jump whose recovered step exceeds MaxStepTicks: the true
	// sequence could be a wrap, a gap-plus-wrap, or a multi-period jump, so
	// no unique recovery exists.
	blocks := []timeline.BlockInput{mk(90), mk(80)}
	res, err := timeline.Merge(blocks, []timeline.TrackInput{spec}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Trusted {
		t.Fatal("ambiguous rollover must mark the timeline untrusted")
	}
	if res.Tracks[videoTrack] == nil {
		t.Fatal("track timeline must still be retained for review")
	}
}
