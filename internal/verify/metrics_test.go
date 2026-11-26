package verify

import (
	"errors"
	"testing"
)

func TestBitrateMetrics(t *testing.T) {
	// 1 second, exactly 100 kbps => 12,500 bytes (100_000 bits / 8).
	m, err := BitrateMetrics(12500, 1_000_000_000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if m.ActualKbps != 100 || m.BitrateDeltaPct != 0 {
		t.Fatalf("exact match wrong: %+v", m)
	}
	// +10% over target: 110 kbps for one second = 13,750 bytes.
	m2, _ := BitrateMetrics(13750, 1_000_000_000, 100)
	if m2.ActualKbps != 110 || m2.BitrateDeltaPct != 10 {
		t.Fatalf("+10%% wrong: %+v", m2)
	}
	// 5 seconds of 200 kbps => 125,000 bytes.
	m3, _ := BitrateMetrics(125000, 5_000_000_000, 200)
	if m3.BitrateDeltaPct != 0 {
		t.Fatalf("multi-second wrong: %+v", m3)
	}
}

func TestBitrateMetricsRejectsBadInput(t *testing.T) {
	if _, err := BitrateMetrics(100, 1_000_000_000, 0); err == nil {
		t.Fatal("zero target must error")
	}
	if _, err := BitrateMetrics(100, 0, 100); err == nil {
		t.Fatal("zero duration must error")
	}
}

func TestBitrateWithinTenPercent(t *testing.T) {
	cases := []struct {
		delta float64
		want  bool
	}{
		{0, true}, {9.99, true}, {-10, true}, {10.01, false}, {-10.01, false},
	}
	for _, c := range cases {
		if got := BitrateWithinTolerance(c.delta, 10); got != c.want {
			t.Fatalf("delta %v => %v want %v", c.delta, got, c.want)
		}
	}
}

func TestMaxTimeError(t *testing.T) {
	exp := []int64{0, 1_000_000_000, 2_000_000_000}
	// Largest deviation is 2 ms on the middle event.
	act := []int64{0, 1_002_000_000, 2_001_000_000}
	ms, err := MaxTimeError(exp, act)
	if err != nil || ms != 2.0 {
		t.Fatalf("max time error = %v err=%v", ms, err)
	}
	if _, err := MaxTimeError(exp, act[:1]); err == nil {
		t.Fatal("count mismatch must error")
	}
}

func TestTimeErrorWithinOneMs(t *testing.T) {
	if !TimeErrorWithinTolerance(1.0, 1.0) {
		t.Fatal("1.0 ms must pass a 1 ms bound")
	}
	if TimeErrorWithinTolerance(1.01, 1.0) {
		t.Fatal("1.01 ms must fail a 1 ms bound")
	}
}

func TestFramePSNR(t *testing.T) {
	ref := make([]byte, 256)
	for i := range ref {
		ref[i] = byte(i)
	}
	// Identical planes => the lossless sentinel (a finite, JSON-safe value).
	if v, err := FramePSNR(ref, ref); err != nil || v != PSNRIdentical {
		t.Fatalf("identical plane psnr = %v err=%v", v, err)
	}
	// Constant one-unit noise yields a finite, high PSNR.
	dist := make([]byte, len(ref))
	copy(dist, ref)
	for i := range dist {
		if dist[i] > 0 {
			dist[i]--
		}
	}
	v, err := FramePSNR(ref, dist)
	if err != nil || v == PSNRIdentical || v < 30 {
		t.Fatalf("noisy plane psnr = %v err=%v", v, err)
	}
	if _, err := FramePSNR(ref, dist[:10]); err == nil {
		t.Fatal("size mismatch must error")
	}
}

func TestParsePSNR(t *testing.T) {
	out := "frame:1 PSNR y:40.1 u:41.2 v:42.3 average:41.20 min:.. max:..\n"
	v, err := parsePSNR(out)
	if err != nil || v != 41.20 {
		t.Fatalf("parse = %v err=%v", v, err)
	}
	infOut := "PSNR y:inf u:inf v:inf average:inf\n"
	v2, err := parsePSNR(infOut)
	if err != nil || v2 != PSNRIdentical {
		t.Fatalf("inf parse = %v err=%v", v2, err)
	}
	if _, err := parsePSNR("no summary here"); err == nil {
		t.Fatal("missing summary must error")
	}
}

type stubRunner struct {
	v   float64
	err error
}

func (s stubRunner) PSNR(ref, dist string) (float64, error) { return s.v, s.err }

func TestAcceptance(t *testing.T) {
	good := Metrics{TargetKbps: 100, ActualKbps: 105, BitrateDeltaPct: 5,
		PSNRDB: 35, MaxTimeErrorMs: 0.5}
	if fails := AcceptanceFailures(good); len(fails) != 0 {
		t.Fatalf("good metrics must pass, got %v", fails)
	}
	bad := Metrics{TargetKbps: 100, ActualKbps: 120, BitrateDeltaPct: 20,
		PSNRDB: 25, MaxTimeErrorMs: 2}
	fails := AcceptanceFailures(bad)
	if len(fails) != 3 {
		t.Fatalf("want 3 failures, got %v", fails)
	}
	// Runner error propagates rather than being treated as a metric.
	if _, err := (stubRunner{err: errors.New("boom")}).PSNR("a", "b"); err == nil {
		t.Fatal("runner error must propagate")
	}
}
