package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"mediarun/internal/frames"
	"mediarun/internal/gen"
)

// TestScannerRejectsOversizedFixtureRecord ensures a record claiming more bytes
// than the configured cap is rejected without allocating it.
func TestScannerRejectsOversizedFixtureRecord(t *testing.T) {
	dir := t.TempDir()
	if _, err := gen.Generate(gen.Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "frames.mfr"))
	if err != nil {
		t.Fatal(err)
	}
	damaged := append([]byte(nil), data...)
	// First record declares a 2 MiB payload.
	damaged[26], damaged[27], damaged[28], damaged[29] = 0x00, 0x20, 0x00, 0x00
	sc, err := frames.NewScanner(bytes.NewReader(damaged), int64(len(damaged)), frames.Limits{MaxPayload: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := sc.Next(); err != nil {
			break
		}
	}
	if sc.Faults().CountKind("length_out_of_range") != 1 {
		t.Fatalf("faults = %s", sc.Faults().Summary())
	}
}

// TestLayoutBoundsRejectHugeDimensions keeps allocation bounded by config.
func TestLayoutBoundsRejectHugeDimensions(t *testing.T) {
	dir := t.TempDir()
	if _, err := gen.Generate(gen.Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Height = 100000
	if _, err := New(dir, Inputs{
		FramesPath: filepath.Join(dir, "frames.mfr"),
		AudioPath:  filepath.Join(dir, "audio.rtp"),
	}, cfg); err == nil {
		t.Fatal("oversize height accepted")
	}
	cfg = DefaultConfig()
	cfg.MaxFrameBytes = frames.MaxPayload + 1
	if _, err := New(dir, Inputs{FramesPath: "a", AudioPath: "b"}, cfg); err == nil {
		t.Fatal("frame byte cap above container limit accepted")
	}
	cfg = DefaultConfig()
	cfg.JitterWindow = 1 << 20
	if _, err := New(dir, Inputs{FramesPath: "a", AudioPath: "b"}, cfg); err == nil {
		t.Fatal("huge jitter window accepted")
	}
}
