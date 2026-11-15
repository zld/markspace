package metrics

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCollectorCurveSamplingAndQuantiles(t *testing.T) {
	const n = 5000
	c := New(n)
	c.SetFrames(n)
	for i := 0; i < n; i++ {
		c.AddReceived()
		c.RecordFrame(time.Duration(i)*20*time.Millisecond, i%7,
			time.Duration(i+1)*time.Millisecond, i%5 == 0)
	}
	c.SetConcealment(4, 1200, 10, 7)
	rep := c.Build()

	if rep.Frames != n || rep.Received != n || rep.Played != n {
		t.Fatalf("counters wrong: %+v", rep)
	}
	if len(rep.BufferLevelCurve) == 0 || len(rep.BufferLevelCurve) > maxCurvePoints {
		t.Fatalf("curve points = %d", len(rep.BufferLevelCurve))
	}
	if rep.ConcealedFrames != n/5 {
		t.Fatalf("concealed = %d, want %d", rep.ConcealedFrames, n/5)
	}
	if rep.RecoveryRatio != 0.7 {
		t.Fatalf("recovery ratio = %v, want 0.7", rep.RecoveryRatio)
	}
	if !(rep.LatencyP50US <= rep.LatencyP95US &&
		rep.LatencyP95US <= rep.LatencyP99US &&
		rep.LatencyP99US <= rep.LatencyMaxUS) {
		t.Fatalf("quantiles not ordered: %+v", rep)
	}
	if rep.LatencyMaxUS != int64((n * time.Millisecond).Microseconds()) {
		t.Fatalf("max latency = %d", rep.LatencyMaxUS)
	}
	if err := Validate(rep); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
}

func TestJSONContainsRequiredFields(t *testing.T) {
	c := New(10)
	c.SetFrames(10)
	d := c.Drops()
	d.Corrupt, d.Duplicate, d.Late = 1, 2, 3
	c.SetConcealment(2, 10, 2, 1)
	rep := c.Build()

	var buf bytes.Buffer
	if err := WriteJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{
		"frames", "received", "duplicate_drops", "late_drops",
		"invalid_packets", "concealed_frames", "max_conceal_run",
		"recovery_ratio", "buffer_level_curve",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("JSON missing field %q", key)
		}
	}
}

func TestTextSummary(t *testing.T) {
	c := New(4)
	c.SetFrames(4)
	for i := 0; i < 4; i++ {
		c.RecordFrame(time.Duration(i)*20*time.Millisecond, 3, 5*time.Millisecond, false)
	}
	rep := c.Build()
	var buf bytes.Buffer
	if err := WriteText(&buf, rep); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, token := range []string{
		"frames=4", "received=0", "duplicate_drops=0", "late_drops=0",
		"invalid_packets=0", "concealed_frames=0", "recovery_ratio=0.0000",
		"buffer_level_curve_points=4",
	} {
		if !strings.Contains(out, token) {
			t.Errorf("text output missing %q", token)
		}
	}
}

func TestValidateDetectsInconsistency(t *testing.T) {
	bad := Report{Frames: 1, Played: 2}
	if err := Validate(bad); err == nil {
		t.Fatal("played > frames not detected")
	}
	bad = Report{Played: 1, ConcealedFrames: 2, RecoveryRatio: 2}
	if err := Validate(bad); err == nil {
		t.Fatal("multiple inconsistencies not detected")
	}
	ok := Report{Frames: 3, Played: 2, ConcealedFrames: 1, RecoveryRatio: 0.5}
	if err := Validate(ok); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
}
