package pipeline

import (
	"encoding/binary"
	"errors"
	"testing"

	"mediapipe/internal/clock"
	"mediapipe/internal/fixtures"
	"mediapipe/internal/mp4"
)

func TestCheckDurationsAcceptsGeneratedFixture(t *testing.T) {
	sc, err := fixtures.Build(4)
	if err != nil {
		t.Fatal(err)
	}
	f, err := mp4.ParseFile(sc.MP4)
	if err != nil {
		t.Fatal(err)
	}
	// The generated moov declares no duration: fragments own the timing.
	if err := CheckDurations(f); err != nil {
		t.Fatalf("generated fixture rejected: %v", err)
	}
	// With a truthful declaration the check must still pass. Video media runs
	// 72 000 ticks; audio starts at 1920 and runs 38 400 ticks.
	f.Tracks[0].Duration = 72000
	f.Tracks[1].Duration = 1920 + 38400
	if err := CheckDurations(f); err != nil {
		t.Fatalf("truthful declaration rejected: %v", err)
	}
}

func TestCheckDurationsRejectsShortDeclaration(t *testing.T) {
	sc, err := fixtures.Build(4)
	if err != nil {
		t.Fatal(err)
	}
	f, err := mp4.ParseFile(sc.MP4)
	if err != nil {
		t.Fatal(err)
	}
	f.Tracks[0].Duration = 9000 // far less than the media actually present
	err = CheckDurations(f)
	if !errors.Is(err, clock.ErrDurationOverflow) {
		t.Fatalf("short declaration accepted: %v", err)
	}
}

func TestCheckDurationsRejectsMovieOverflow(t *testing.T) {
	sc, err := fixtures.Build(4)
	if err != nil {
		t.Fatal(err)
	}
	f, err := mp4.ParseFile(sc.MP4)
	if err != nil {
		t.Fatal(err)
	}
	f.Duration = 1
	if err := CheckDurations(f); !errors.Is(err, clock.ErrDurationOverflow) {
		t.Fatalf("movie overflow accepted: %v", err)
	}
}

func TestCheckDurationsRejectsZeroTimescale(t *testing.T) {
	if err := CheckDurations(&mp4.File{}); !errors.Is(err, clock.ErrZeroTimescale) {
		t.Fatalf("zero timescale accepted: %v", err)
	}
}

func TestCorruptedMVHDDurationDetected(t *testing.T) {
	sc, err := fixtures.Build(4)
	if err != nil {
		t.Fatal(err)
	}
	data := append([]byte(nil), sc.MP4...)
	// Locate mvhd inside moov and rewrite its duration to 1 tick.
	idx := findBox(data, "mvhd")
	if idx < 0 {
		t.Fatal("no mvhd")
	}
	// mvhd v0 payload: size(4) type(4) ver/flags(4) ctime(4) mtime(4) ts(4) dur(4)
	binary.BigEndian.PutUint32(data[idx+24:], 1)
	f, err := mp4.ParseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Duration != 1 {
		t.Fatalf("corruption not applied: %d", f.Duration)
	}
	if err := CheckDurations(f); !errors.Is(err, clock.ErrDurationOverflow) {
		t.Fatalf("corrupt duration accepted: %v", err)
	}
}

func findBox(data []byte, typ string) int {
	for i := 0; i+8 <= len(data); i++ {
		if string(data[i+4:i+8]) == typ {
			return i
		}
	}
	return -1
}
