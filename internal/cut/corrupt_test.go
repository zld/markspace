package cut_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"hlscheck/internal/cut"
	"hlscheck/internal/demo"
	"hlscheck/internal/ts"
)

// A corrupted sync byte isolates the profile: slicing fails and
// reports the offset.
func TestSyncErrorIsolatesProfile(t *testing.T) {
	data := gen(t, demo.GenOptions{BitrateKbps: 450, DurationSec: 8, GOPSec: 2, Seed: 6})
	data[10*ts.PacketSize] = 0x00 // break the sync byte of packet 10
	_, err := cut.Slice(context.Background(), bytes.NewReader(data), t.TempDir(), cut.Options{TargetDuration: 4})
	if err == nil || !strings.Contains(err.Error(), "sync byte error") {
		t.Fatalf("want sync byte error, got %v", err)
	}
	if !strings.Contains(err.Error(), "offset") {
		t.Errorf("error should carry an offset: %v", err)
	}
}

// A truncated tail fails the slice with the byte offset.
func TestTruncatedInput(t *testing.T) {
	data := gen(t, demo.GenOptions{BitrateKbps: 450, DurationSec: 8, GOPSec: 2, Seed: 7})
	data = data[:len(data)-100] // partial final packet
	_, err := cut.Slice(context.Background(), bytes.NewReader(data), t.TempDir(), cut.Options{TargetDuration: 4})
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("want truncated error, got %v", err)
	}
}
