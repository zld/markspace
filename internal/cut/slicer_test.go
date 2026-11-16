package cut_test

import (
	"bytes"
	"context"
	"testing"

	"hlscheck/internal/cut"
	"hlscheck/internal/demo"
	"hlscheck/internal/ts"
)

func gen(t *testing.T, opt demo.GenOptions) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := demo.Generate(&buf, opt); err != nil {
		t.Fatalf("generate: %v", err)
	}
	return buf.Bytes()
}

func TestSliceBoundaries(t *testing.T) {
	data := gen(t, demo.GenOptions{BitrateKbps: 450, DurationSec: 12, GOPSec: 2, Seed: 1})
	res, err := cut.Slice(context.Background(), bytes.NewReader(data), t.TempDir(), cut.Options{TargetDuration: 4})
	if err != nil {
		t.Fatalf("slice: %v", err)
	}
	if len(res.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(res.Segments))
	}
	start := int64(ts.ClockRate * 3600)
	for i, s := range res.Segments {
		want := start + int64(i)*4*ts.ClockRate
		if s.StartPTS != want {
			t.Errorf("seg %d start = %d, want %d (delta %d ticks)", i, s.StartPTS, want, s.StartPTS-want)
		}
		if s.Duration < 3.9 || s.Duration > 4.1 {
			t.Errorf("seg %d duration = %.3f", i, s.Duration)
		}
	}
}
