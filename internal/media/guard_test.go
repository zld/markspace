package media

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestDecodeStopsAtTimeout pins the subprocess guard: a decoder that never
// produces audio must be killed and reported, not allowed to hang the batch.
func TestDecodeStopsAtTimeout(t *testing.T) {
	info := Info{SampleRate: 48000, Channels: 2}
	start := time.Now()
	err := Decode(context.Background(), "/dev/null", info, DecodeOptions{Timeout: 200 * time.Millisecond}, func([]float64) error { return nil })
	if err == nil {
		t.Fatal("expected a decode error for an empty stream")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("decode took %v, timeout was not enforced", elapsed)
	}
}

// TestDecodePropagatesSinkError checks that a DSP failure stops the decoder
// and reaches the caller unchanged, so the batch layer can name the stage.
func TestDecodePropagatesSinkError(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "one.wav")
	writeFloatWAV(t, path, 1, 48000, make([]float64, 4800))
	info := Info{SampleRate: 48000, Channels: 1}
	want := errors.New("sink exploded")
	calls := 0
	err := Decode(context.Background(), path, info, DecodeOptions{}, func([]float64) error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want the sink error", err)
	}
	if calls != 1 {
		t.Errorf("sink called %d times after returning an error", calls)
	}
}
