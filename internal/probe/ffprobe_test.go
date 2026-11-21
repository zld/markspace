package probe

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeWAV writes a deterministic 8 kHz mono PCM sine WAV of the given
// duration, giving ffprobe an exact, dependency-free input.
func writeWAV(t *testing.T, path string, ms int64) {
	t.Helper()
	const sampleRate = 8000
	samples := int(int64(sampleRate) * ms / 1000)
	dataLen := samples * 2
	b := make([]byte, 44+dataLen)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+dataLen))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], sampleRate)
	binary.LittleEndian.PutUint32(b[28:], sampleRate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(dataLen))
	for i := 0; i < samples; i++ {
		v := int16(math.Sin(2*math.Pi*440*float64(i)/sampleRate) * 8000)
		binary.LittleEndian.PutUint16(b[44+i*2:], uint16(v))
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeFFprobe creates a shell script that behaves like a limited ffprobe.
func fakeFFprobe(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDurationReadsRealWAV(t *testing.T) {
	if err := Available(""); err != nil {
		t.Skip(err)
	}
	path := filepath.Join(t.TempDir(), "a.wav")
	writeWAV(t, path, 4000)
	ms, err := Duration(context.Background(), "", path, 0)
	if err != nil {
		t.Fatalf("Duration: %v", err)
	}
	if ms != 4000 {
		t.Fatalf("Duration = %d, want 4000", ms)
	}
}

func TestDurationReportsFailures(t *testing.T) {
	notMedia := filepath.Join(t.TempDir(), "bad.ts")
	if err := os.WriteFile(notMedia, []byte("not media"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Duration(context.Background(), "", notMedia, 0); err == nil {
		t.Fatal("Duration accepted non-media input")
	}

	slow := fakeFFprobe(t, "sleep 2\n")
	start := time.Now()
	_, err := Duration(context.Background(), slow, notMedia, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("timeout error = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout was not enforced")
	}

	zero := fakeFFprobe(t, `echo 0.000000`)
	if _, err := Duration(context.Background(), zero, notMedia, 0); err == nil {
		t.Fatal("Duration accepted zero duration")
	}
	missing := fakeFFprobe(t, `echo N/A`)
	if _, err := Duration(context.Background(), missing, notMedia, 0); err == nil {
		t.Fatal("Duration accepted N/A")
	}
	failing := fakeFFprobe(t, `echo "boom" >&2; exit 3`)
	_, err = Duration(context.Background(), failing, notMedia, 0)
	var cmdErr *CommandError
	if err == nil || !strings.Contains(err.Error(), "status 3") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("command error = %v", err)
	}
	_ = cmdErr
}

func TestAvailable(t *testing.T) {
	if err := Available(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("Available accepted missing binary")
	}
}
