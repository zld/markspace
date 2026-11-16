package cut_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"hlscheck/internal/cut"
	"hlscheck/internal/demo"
	"hlscheck/internal/ts"
)

// Sparse keyframes: with a 6 s GOP and a 4 s target, the cut lands on
// the first keyframe past the target, i.e. every 6 s.
func TestSparseKeyframes(t *testing.T) {
	data := gen(t, demo.GenOptions{BitrateKbps: 450, DurationSec: 18, GOPSec: 6, Seed: 2})
	res, err := cut.Slice(context.Background(), bytes.NewReader(data), t.TempDir(), cut.Options{TargetDuration: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(res.Segments))
	}
	for i, s := range res.Segments {
		if s.Duration < 5.9 || s.Duration > 6.1 {
			t.Errorf("seg %d duration = %.3f, want ~6", i, s.Duration)
		}
	}
}

// Input without any keyframe cannot be sliced.
func TestNoKeyframes(t *testing.T) {
	data := gen(t, demo.GenOptions{BitrateKbps: 450, DurationSec: 8, GOPSec: -1, Seed: 3})
	dir := t.TempDir()
	_, err := cut.Slice(context.Background(), bytes.NewReader(data), dir, cut.Options{TargetDuration: 4})
	if err == nil {
		t.Fatal("want error for keyframe-less input")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("no output should remain, got %v", entries)
	}
}

// A short final tail is kept as its own segment.
func TestShortTail(t *testing.T) {
	data := gen(t, demo.GenOptions{BitrateKbps: 450, DurationSec: 9.5, GOPSec: 2, Seed: 4})
	res, err := cut.Slice(context.Background(), bytes.NewReader(data), t.TempDir(), cut.Options{TargetDuration: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(res.Segments))
	}
	tail := res.Segments[2]
	if tail.Duration < 1.4 || tail.Duration > 1.6 {
		t.Errorf("tail duration = %.3f, want ~1.5", tail.Duration)
	}
	start := int64(ts.ClockRate * 3600)
	if tail.StartPTS != start+8*ts.ClockRate {
		t.Errorf("tail start = %d", tail.StartPTS)
	}
}

// Cancellation stops the slice without publishing the open segment.
func TestCancel(t *testing.T) {
	data := gen(t, demo.GenOptions{BitrateKbps: 1800, DurationSec: 20, GOPSec: 2, Seed: 5})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	_, err := cut.Slice(ctx, bytes.NewReader(data), dir, cut.Options{TargetDuration: 4})
	if err == nil {
		t.Fatal("want cancellation error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("temp files left after cancel: %v", entries)
	}
}
