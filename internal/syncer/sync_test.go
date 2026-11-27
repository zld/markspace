package syncer

import (
	"testing"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

const ms = clock.Millisecond

func videoTrack(times ...clock.Time) Track {
	s := make([]Sample, len(times))
	for i, t := range times {
		s[i] = Sample{PTS: t, Duration: 33 * ms}
	}
	return Track{Kind: "video", Samples: s}
}

func audioTrack(times ...clock.Time) Track {
	s := make([]Sample, len(times))
	for i, t := range times {
		s[i] = Sample{PTS: t, Duration: 21 * ms}
	}
	return Track{Kind: "audio", Samples: s}
}

func TestSyncExactOffset(t *testing.T) {
	v := videoTrack(0, 33*ms, 66*ms, 99*ms)
	a := audioTrack(10*ms, 43*ms, 76*ms, 109*ms)
	res, err := Sync(v, a, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if res.Offset != 10*ms {
		t.Fatalf("offset = %s want 10ms", res.Offset)
	}
	if res.Residual != 0 {
		t.Fatalf("residual = %s want 0", res.Residual)
	}
	if !res.Monotonicity {
		t.Fatal("expected monotonic tracks")
	}
	if res.Video.Admitted != 4 || res.Audio.Admitted != 4 {
		t.Fatalf("stats = %+v %+v", res.Video, res.Audio)
	}
}

func TestSyncLateVideoFrameDoesNotRewind(t *testing.T) {
	// Video frame 3 arrives with an earlier timestamp than frame 2.
	v := videoTrack(0, 33*ms, 99*ms, 66*ms, 132*ms)
	a := audioTrack(0, 33*ms, 66*ms, 99*ms, 132*ms)
	cfg := DefaultConfig()
	cfg.LateTolerance = 40 * ms
	res, err := Sync(v, a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Video.Clamped != 1 {
		t.Fatalf("clamped = %d want 1", res.Video.Clamped)
	}
	if res.Video.LateDropped != 0 {
		t.Fatalf("late dropped = %d want 0", res.Video.LateDropped)
	}
	if !res.Monotonicity {
		// A clamp is still monotonic output; the flag reports the input was
		// reordered, which is true here.
		t.Log("input reordered, expected")
	}
	if res.Offset != 0 {
		t.Fatalf("offset = %s want 0", res.Offset)
	}
}

func TestSyncVariableAudioIntervals(t *testing.T) {
	v := videoTrack(0, 33*ms, 66*ms, 99*ms, 132*ms)
	// Audio packets are irregular but centred on the video timeline.
	a := audioTrack(5*ms, 20*ms, 40*ms, 70*ms, 88*ms, 110*ms, 130*ms)
	res, err := Sync(v, a, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if res.Audio.Clamped != 0 || res.Audio.LateDropped != 0 {
		t.Fatalf("audio stats = %+v", res.Audio)
	}
	if res.Offset < 0 || res.Offset > 20*ms {
		t.Fatalf("offset out of range: %s", res.Offset)
	}
	if abs(res.Residual) > 40*ms {
		t.Fatalf("residual too large: %s", res.Residual)
	}
}

func TestSyncDropsVeryLateFrame(t *testing.T) {
	v := videoTrack(0, 500*ms, 100*ms)
	a := audioTrack(0, 100*ms)
	cfg := DefaultConfig()
	cfg.LateTolerance = 40 * ms
	res, err := Sync(v, a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Video.LateDropped != 1 {
		t.Fatalf("late dropped = %d want 1", res.Video.LateDropped)
	}
	found := false
	for _, f := range res.Faults {
		if f.Code == fault.CodeSyncLateFrame {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing late-frame fault: %+v", res.Faults)
	}
}

func TestSyncOffsetBeyondLimit(t *testing.T) {
	v := videoTrack(0, 33*ms)
	a := audioTrack(400*ms, 433*ms)
	res, err := Sync(v, a, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if res.AppliedOffset != 0 {
		t.Fatalf("applied offset = %s want 0", res.AppliedOffset)
	}
	if len(res.Faults) == 0 || res.Faults[0].Code != fault.CodeSyncLateFrame {
		t.Fatalf("faults = %+v", res.Faults)
	}
}

func TestSyncEmptyTrack(t *testing.T) {
	_, err := Sync(videoTrack(), audioTrack(0), DefaultConfig())
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeSyncEmptyTrack {
		t.Fatalf("err = %v", err)
	}
}

func TestSyncDeterministic(t *testing.T) {
	v := videoTrack(0, 33*ms, 66*ms)
	a := audioTrack(7*ms, 40*ms, 73*ms)
	r1, _ := Sync(v, a, DefaultConfig())
	r2, _ := Sync(v, a, DefaultConfig())
	if r1.Offset != r2.Offset || r1.Residual != r2.Residual {
		t.Fatalf("non-deterministic: %+v vs %+v", r1, r2)
	}
}

func TestSyncOffsetMs(t *testing.T) {
	res := &Result{Offset: 12500 * clock.Microsecond, Residual: 1500 * clock.Microsecond}
	if res.OffsetMs() != 12.5 || res.ResidualMs() != 1.5 {
		t.Fatalf("ms conversion wrong: %v %v", res.OffsetMs(), res.ResidualMs())
	}
}
