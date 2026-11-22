package timeline

import "testing"

func testConfig() RepairConfig {
	return RepairConfig{WrapThresholdNs: 1_000_000_000, MaxSkewNs: 0, MinStepNs: 1}
}

func TestRebaseNonZeroStart(t *testing.T) {
	r := NewRepairer(NewClock(1_000_000), testConfig())
	ns, err := r.Process(1, KindVideo, 500, 0) // starts at 500ms
	if err != nil {
		t.Fatal(err)
	}
	if ns != 0 {
		t.Fatalf("expected rebase to 0, got %d", ns)
	}
	if r.Stats.Rebased != 1 {
		t.Fatalf("expected 1 rebase, got %d", r.Stats.Rebased)
	}
	ns, _ = r.Process(1, KindVideo, 533, 0)
	if ns != 33_000_000 {
		t.Fatalf("expected 33ms, got %d", ns)
	}
}

func TestWrapRepairKeepsMonotonic(t *testing.T) {
	r := NewRepairer(NewClock(1_000_000), testConfig())
	// muxer restart: 66s jumps back to 5ms, beyond the 1s wrap threshold
	seq := []int64{0, 33000, 66000, 5, 38}
	want := []int64{0, 33_000_000_000, 66_000_000_000, 66_000_000_001, 66_033_000_001}
	for i, raw := range seq {
		ns, err := r.Process(1, KindVideo, raw, int64(i))
		if err != nil {
			t.Fatal(err)
		}
		if ns != want[i] {
			t.Fatalf("sample %d: want %d got %d", i, want[i], ns)
		}
	}
	if r.Stats.Wraps != 1 {
		t.Fatalf("expected 1 wrap, got %d", r.Stats.Wraps)
	}
}

func TestRegressionClamped(t *testing.T) {
	r := NewRepairer(NewClock(1_000_000), testConfig())
	seq := []int64{0, 40, 38, 41} // small local regression at 38
	var last int64
	for i, raw := range seq {
		ns, err := r.Process(2, KindAudio, raw, int64(i))
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && ns <= last {
			t.Fatalf("sample %d not monotonic: %d <= %d", i, ns, last)
		}
		last = ns
	}
	if r.Stats.Regressions != 1 {
		t.Fatalf("expected 1 regression, got %d", r.Stats.Regressions)
	}
}

func TestSkewExceededFails(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSkewNs = 10_000_000 // 10ms
	r := NewRepairer(NewClock(1_000_000), cfg)
	// Skew is sampled at video samples against the latest audio.
	r.Process(1, KindVideo, 0, 0)
	r.Process(2, KindAudio, 0, 0)
	_, err := r.Process(1, KindVideo, 50, 8) // video 50ms ahead of audio
	f := AsFault(err)
	if f == nil || f.Class != ClassSyncExceeded {
		t.Fatalf("expected sync_exceeded fault, got %v", err)
	}
	if f.Offset != 8 {
		t.Fatalf("fault offset = %d, want 8", f.Offset)
	}
	if f.Class.ExitCode() != 2 {
		t.Fatalf("sync_exceeded exit code = %d, want 2", f.Class.ExitCode())
	}
}

func TestSkewWithinLimitTracked(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSkewNs = 40_000_000
	r := NewRepairer(NewClock(1_000_000), cfg)
	r.Process(1, KindVideo, 0, 0)
	r.Process(2, KindAudio, 0, 0)
	r.Process(2, KindAudio, 2, 0)
	if _, err := r.Process(1, KindVideo, 4, 0); err != nil {
		t.Fatal(err)
	}
	if r.Stats.MaxSkewNs != 2_000_000 {
		t.Fatalf("max skew = %d, want 2ms", r.Stats.MaxSkewNs)
	}
}

func TestFaultExitCodes(t *testing.T) {
	cases := map[Class]int{
		ClassTruncated:     2,
		ClassSizeOverflow:  2,
		ClassSyncExceeded:  2,
		ClassTrackMapping:  2,
		ClassCodec:         2,
		ClassInterrupted:   3,
		ClassInternal:      1,
		ClassIndexMismatch: 1,
	}
	for c, want := range cases {
		if got := c.ExitCode(); got != want {
			t.Errorf("%s: exit %d, want %d", c, got, want)
		}
	}
}
