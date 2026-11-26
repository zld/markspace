package media_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/fixture"
	"mediad/internal/media"
)

func TestParseRTPBasicTiming(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.rtp")
	spec := fixture.RTPSpec{
		Path:       path,
		ClockRate:  90000,
		Timestamps: []uint32{0, 3000, 6000, 9000},
		Payloads:   makePayloads(4, 24),
		SSRC:       1,
	}
	if err := fixture.WriteRTP(spec); err != nil {
		t.Fatal(err)
	}
	track, err := media.ParseRTP(path, "rtp-audio")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(track.Samples) != 4 {
		t.Fatalf("samples = %d", len(track.Samples))
	}
	// 3000 ticks at 90 kHz = 1/30 s = 33,333,333 ns.
	wantNs := int64(33333333)
	if track.Samples[0].DurationNs != wantNs {
		t.Fatalf("first duration = %d, want %d", track.Samples[0].DurationNs, wantNs)
	}
	if track.Samples[1].StartNs != wantNs {
		t.Fatalf("second start = %d, want %d", track.Samples[1].StartNs, wantNs)
	}
}

func TestParseRTPTimestampWrap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrap.rtp")
	// Start 1000 ticks below the 32-bit boundary, step 500 -> one wrap.
	ts := fixture.WrapTimestamps(^uint32(0)-999, 500, 5)
	spec := fixture.RTPSpec{
		Path:       path,
		ClockRate:  1000, // 1 tick = 1 ms for simple nanosecond math
		Timestamps: ts,
		Payloads:   makePayloads(5, 16),
		SSRC:       2,
	}
	if err := fixture.WriteRTP(spec); err != nil {
		t.Fatal(err)
	}
	track, err := media.ParseRTP(path, "wrap")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	// Every delta is 500 ticks = 500 ms; monotonic starts must keep growing.
	for i, s := range track.Samples {
		if s.DurationNs != 500*media.NanoPerSecond/1000 {
			t.Fatalf("sample %d duration = %d", i, s.DurationNs)
		}
		if i > 0 && s.StartNs <= track.Samples[i-1].StartNs {
			t.Fatalf("sample %d start regressed across wrap", i)
		}
	}
	if last := track.Samples[4].StartNs; last != 2_000_000_000 {
		t.Fatalf("post-wrap start = %d, want 2s", last)
	}
}

func TestParseRTPZeroClockRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zero.rtp")
	if err := fixture.WriteRTP(fixture.RTPSpec{
		Path:       path,
		ClockRate:  1, // valid file first
		Timestamps: []uint32{0, 10},
		Payloads:   makePayloads(2, 8),
	}); err != nil {
		t.Fatal(err)
	}
	// Corrupt the clock-rate field at byte offset 4 (magic occupies 0-3).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[4], data[5], data[6], data[7] = 0, 0, 0, 0
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = media.ParseRTP(path, "x")
	var me *media.Error
	if !errors.As(err, &me) {
		t.Fatalf("want structured error, got %v", err)
	}
	if me.Stage != media.StageScan || me.InputOffset != 4 {
		t.Fatalf("zero clock error not locatable: %+v", me)
	}
}
