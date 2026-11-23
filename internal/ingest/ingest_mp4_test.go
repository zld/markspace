package ingest

import (
	"path/filepath"
	"testing"

	"mediapipe/internal/fault"
	"mediapipe/internal/testdata"
)

func fixtureDir(t *testing.T) string {
	dir := t.TempDir()
	return dir
}

func buildMP4Fixture(t *testing.T, dir string, dupPTS int) string {
	t.Helper()
	audio := testdata.NewAudio(42, 48000, 4_000_000_000)
	frames := testdata.NewFrames(42, 90, 64, 48, 12)
	r := testdata.NewRand(42)
	pts, dts, dur := testdata.VFRTiming(r, len(frames), 1_000_000_000/30,
		500_000_000, dupPTS)
	spec := testdata.MP4Spec{
		Seed: 42, Width: 64, Height: 48, Timescale: 30000,
		Audio: audio, Frames: frames, VideoPTS: pts, VideoDTS: dts,
		VideoDur: dur, StartNanos: 500_000_000, Rotation: 90,
		Crop:   [4]int64{2, 2, 2, 2},
		Colors: []testdata.ColorSpec{{Primaries: 9, Transfer: 16, Matrix: 9, FullRange: true}, {Primaries: 1, Transfer: 1, Matrix: 1, FullRange: false}},
	}
	path := filepath.Join(dir, "clip.mp4")
	if err := testdata.WriteMP4(path, spec); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseMP4HappyPath(t *testing.T) {
	dir := fixtureDir(t)
	path := buildMP4Fixture(t, dir, -1)
	var errs fault.Batch
	tracks, err := ParseMP4(path, &errs)
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(tracks))
	}
	var video, audioCount int
	for _, tr := range tracks {
		if tr.Container != "mp4" || tr.SourceFile != path {
			t.Errorf("track mapping not traceable: %+v", tr)
		}
		if tr.Kind == "video" {
			video++
			if tr.Rotation.Degrees != 90 {
				t.Errorf("rotation = %d, want 90", tr.Rotation.Degrees)
			}
			if tr.Rotation.SourceOffset < 0 {
				t.Errorf("rotation missing source offset")
			}
			if tr.Crop == nil {
				t.Error("crop not parsed")
			}
			if len(tr.Color.Declarations) != 2 {
				t.Errorf("color declarations = %d, want 2",
					len(tr.Color.Declarations))
			}
			if len(tr.Samples) != 90 {
				t.Errorf("video samples = %d, want 90", len(tr.Samples))
			}
			// Non-zero start must be reflected and durations non-negative.
			for _, s := range tr.Samples {
				if s.DurationNanos < 0 {
					t.Fatal("negative duration")
				}
				if s.PTS < 500_000_000-1_000_000 {
					t.Fatalf("edit-list start not applied: pts=%d", s.PTS)
				}
			}
		} else {
			audioCount++
			if len(tr.Samples) == 0 {
				t.Error("audio has no samples")
			}
		}
	}
	if video != 1 || audioCount != 1 {
		t.Fatalf("want 1 video + 1 audio, got %d/%d", video, audioCount)
	}
	if errs.Len() != 0 {
		t.Fatalf("unexpected faults: %v", errs.All())
	}
}

func TestParseMP4TruncatedBox(t *testing.T) {
	dir := fixtureDir(t)
	path := buildMP4Fixture(t, dir, -1)
	dst := filepath.Join(dir, "trunc.mp4")
	off, err := testdata.TruncateBox(path, dst)
	if err != nil {
		t.Fatal(err)
	}
	var errs fault.Batch
	tracks, _ := ParseMP4(dst, &errs)
	if !errs.HasKind(fault.KindTruncated) {
		t.Fatalf("want truncated fault, got %v", errs.All())
	}
	found := false
	for _, e := range errs.All() {
		if e.Kind == fault.KindTruncated {
			if e.Offset != off {
				t.Errorf("offset = %d, want %d", e.Offset, off)
			}
			if !e.Recoverable {
				t.Error("truncation must be recoverable")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no recoverable truncated-unit error")
	}
	// moov precedes the truncated tail in our fixture, so tracks survive.
	if len(tracks) == 0 {
		t.Fatal("complete tracks should still be processed")
	}
}

func TestParseMP4DuplicatePTS(t *testing.T) {
	dir := fixtureDir(t)
	path := buildMP4Fixture(t, dir, 7)
	var errs fault.Batch
	tracks, err := ParseMP4(path, &errs)
	if err != nil {
		t.Fatal(err)
	}
	if !errs.HasKind(fault.KindVFRDupPTS) {
		t.Fatalf("want dup-pts fault, got %v", errs.All())
	}
	for _, tr := range tracks {
		for _, s := range tr.Samples {
			if s.DurationNanos < 0 {
				t.Fatal("negative duration under duplicate PTS")
			}
		}
	}
}
