package remux_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/adapter"
	mp4a "mediacheck/internal/adapter/mp4"
	"mediacheck/internal/clock"
	"mediacheck/internal/fault"
	"mediacheck/internal/manifest"
	"mediacheck/internal/remux"
)

func makeSpecs() []remux.TrackSpec {
	var video, audio []adapter.Sample
	// 10 video frames @40ms (90kHz), keyframe every 5, small CTS offset.
	for i := 0; i < 10; i++ {
		video = append(video, adapter.Sample{
			DTS:      int64(i) * 3600,
			CTS:      int64(i)*3600 + 1800,
			Duration: 3600,
			Size:     uint32(1000 + i*17),
			Keyframe: i%5 == 0,
		})
	}
	// Audio frames of 960 ticks (20ms) @48kHz covering the same 400ms.
	var acc int64
	for acc < 19200 {
		audio = append(audio, adapter.Sample{
			DTS: acc, CTS: acc, Duration: 960, Size: 256,
		})
		acc += 960
	}
	return []remux.TrackSpec{
		{ID: 1, Kind: adapter.KindVideo, Timescale: 90000, Samples: video},
		{ID: 2, Kind: adapter.KindAudio, Timescale: 48000, Samples: audio},
	}
}

func remuxToFile(t *testing.T, specs []remux.TrackSpec, durNanos int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := remux.New(f)
	if err := w.WriteHeader(specs, durNanos); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	for ti, spec := range specs {
		for _, s := range spec.Samples {
			if err := w.WriteSample(ti, make([]byte, s.Size)); err != nil {
				t.Fatalf("WriteSample: %v", err)
			}
		}
	}
	if _, err := w.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	return path
}

func TestRemuxParseRoundTrip(t *testing.T) {
	specs := makeSpecs()
	durNanos := clock.Timescale(90000).ToNanos(36000)
	path := remuxToFile(t, specs, durNanos)

	fl := fault.NewList()
	media, err := mp4a.ParserV1{}.Parse(context.Background(), path, fl)
	if err != nil {
		t.Fatalf("parse remuxed file: %v", err)
	}
	defer media.Close()
	if len(media.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(media.Tracks))
	}
	for i, spec := range specs {
		got := media.Tracks[i]
		if got.Timescale != spec.Timescale {
			t.Fatalf("track %d timescale = %d, want %d", i, got.Timescale, spec.Timescale)
		}
		if len(got.Samples) != len(spec.Samples) {
			t.Fatalf("track %d samples = %d, want %d", i, len(got.Samples), len(spec.Samples))
		}
		for j, s := range spec.Samples {
			g := got.Samples[j]
			if g.DTS != s.DTS || g.CTS != s.CTS || g.Duration != s.Duration ||
				g.Size != s.Size || g.Keyframe != s.Keyframe {
				t.Fatalf("track %d sample %d = %+v, want %+v", i, j, g, s)
			}
		}
	}
	if media.DurationNanos != durNanos {
		t.Fatalf("duration = %d ns, want %d ns", media.DurationNanos, durNanos)
	}
}

func TestManifestBoundaryValidation(t *testing.T) {
	durNanos := clock.Timescale(90000).ToNanos(36000) // 400ms
	frame := clock.Timescale(90000).ToNanos(3600)     // 40ms

	m := &manifest.Manifest{Source: "test"}
	m.Add(manifest.Entry{StartNanos: 0, EndNanos: durNanos / 2, Status: manifest.StatusOK})
	m.Add(manifest.Entry{StartNanos: durNanos / 2, EndNanos: durNanos, Status: manifest.StatusOK})
	if err := m.Validate(durNanos, frame); err != nil {
		t.Fatalf("exact manifest rejected: %v", err)
	}
	if got := m.BoundaryError(durNanos); got != 0 {
		t.Fatalf("boundary error = %d, want 0", got)
	}

	// Ending one frame + 1ns late exceeds max(frame, 10ms) and must fail.
	bad := &manifest.Manifest{Source: "test"}
	bad.Add(manifest.Entry{StartNanos: 0, EndNanos: durNanos + frame + 1, Status: manifest.StatusOK})
	if err := bad.Validate(durNanos, frame); err == nil {
		t.Fatal("overshooting manifest must fail validation")
	}

	// 5ms early end is inside the 10ms floor and must pass.
	ok := &manifest.Manifest{Source: "test"}
	ok.Add(manifest.Entry{StartNanos: 0, EndNanos: durNanos - 5_000_000, Status: manifest.StatusOK})
	if err := ok.Validate(durNanos, frame); err != nil {
		t.Fatalf("5ms deviation must be tolerated: %v", err)
	}
}

func TestManifestJSONDeterministic(t *testing.T) {
	m := &manifest.Manifest{Source: "sample"}
	m.Add(manifest.Entry{StartNanos: 0, EndNanos: 40_000_000, Status: manifest.StatusRecovered, AVDeltaNanos: 1200})
	a, err := m.JSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("manifest JSON must be byte-identical across runs")
	}
}
