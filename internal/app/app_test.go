package app

import (
	"testing"
	"time"

	"mediatimeline/internal/container"
)

func TestRunDemoMetrics(t *testing.T) {
	dir := t.TempDir()
	m, err := RunDemo(dir, DemoOptions{Seed: 42, LateMS: 80, CheckpointEvery: 128})
	if err != nil {
		t.Fatal(err)
	}
	if m.EventCount == 0 || m.ResampleFrames == 0 || m.OutputSHA256 == "" {
		t.Fatalf("incomplete metrics: %+v", m)
	}
	if m.WatermarkNS == 0 {
		t.Fatal("watermark never advanced")
	}
	// Determinism: a second identical run yields the same checksum.
	m2, err := RunDemo(t.TempDir(), DemoOptions{Seed: 42, LateMS: 80, CheckpointEvery: 128})
	if err != nil {
		t.Fatal(err)
	}
	if m.OutputSHA256 != m2.OutputSHA256 {
		t.Fatalf("non-deterministic demo: %s vs %s", m.OutputSHA256, m2.OutputSHA256)
	}
}

func TestRunCorruptLocalizes(t *testing.T) {
	cases := []string{"truncated-stbl", "illegal-offset", "bad-crc"}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			loc, err := RunCorrupt(t.TempDir(), kind)
			if err == nil {
				t.Fatal("expected a localized error")
			}
			if loc.TrackID != 1 {
				t.Fatalf("track=%d want 1", loc.TrackID)
			}
			// Localization must be within one sample of the damage.
			if kind == "bad-crc" && loc.SampleIndex != sampleCount/2 {
				t.Fatalf("sample=%d want %d", loc.SampleIndex, sampleCount/2)
			}
			// Per-sample damage reports a byte offset; a whole-table truncated
			// box legitimately carries -1 because no single sample is at fault.
			if kind != "truncated-stbl" && loc.Offset < 0 {
				t.Fatalf("missing offset for %s: %+v", kind, loc)
			}
		})
	}
}

func TestCorruptCheckpointUnrecoverable(t *testing.T) {
	_, err := RunCorrupt(t.TempDir(), "checkpoint")
	if err == nil {
		t.Fatal("expected unrecoverable checkpoint error")
	}
}

func TestRunResumeMatchesChecksum(t *testing.T) {
	m, recoveryMS, replayed, err := RunResume(t.TempDir(), 3*time.Second)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if m.OutputSHA256 == "" {
		t.Fatal("missing resumed checksum")
	}
	if recoveryMS < 0 {
		t.Fatal("negative recovery time")
	}
	if int64(replayed) > m.EventCount {
		t.Fatalf("replayed %d exceeds events %d", replayed, m.EventCount)
	}
}

func TestKillThresholdBounds(t *testing.T) {
	// Zero/negative duration falls back to the 25% minimum fraction.
	events := make([]container.SampleEvent, 100)
	if n := killThreshold(events, 0); n != 25 {
		t.Fatalf("default threshold=%d want 25", n)
	}
	// A long duration is clamped to 90% so some work always remains.
	if n := killThreshold(events, 60*time.Second); n != 90 {
		t.Fatalf("clamped threshold=%d want 90", n)
	}
}
