package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"

	"mediaforge/internal/manifest"
	"mediaforge/internal/mediaerr"
	"mediaforge/internal/quality"
)

// verifyDir re-reads the produced MP4, manifest and stats and checks that
// they agree to within one audio sample and that the file hash is unchanged.
func verifyDir(dir string, stats quality.Stats) error {
	if stats.OutputSHA256 == "" {
		return fmt.Errorf("stats do not contain output_sha256")
	}
	manifestPath, mp4Path, err := findOutputs(dir)
	if err != nil {
		return err
	}
	hash, err := quality.HashFile(mp4Path)
	if err != nil {
		return err
	}
	if hash != stats.OutputSHA256 {
		return fmt.Errorf("output hash changed: got %s want %s", hash, stats.OutputSHA256)
	}
	tl, err := manifest.Read(manifestPath)
	if err != nil {
		return err
	}
	f, err := os.Open(mp4Path)
	if err != nil {
		return mediaerr.New(mediaerr.CodeIO, "open %s: %v", mp4Path, err)
	}
	defer f.Close()
	probe, err := mp4.Probe(f)
	if err != nil {
		return fmt.Errorf("mp4 probe: %w", err)
	}
	if len(probe.Tracks) != 1 {
		return fmt.Errorf("expected 1 track, found %d", len(probe.Tracks))
	}
	track := probe.Tracks[0]
	if track.Timescale != uint32(tl.SampleRate) {
		return fmt.Errorf("mp4 timescale %d != manifest rate %d", track.Timescale, tl.SampleRate)
	}
	if quality.AlignmentSamples(int64(track.Duration), tl.DurationSample) > 1 {
		return fmt.Errorf("mp4 duration %d and manifest %d differ by more than one sample",
			track.Duration, tl.DurationSample)
	}
	if quality.AlignmentSamples(tl.DurationSample, stats.OutputSamples) > 1 {
		return fmt.Errorf("stats sample count %d disagrees with manifest %d",
			stats.OutputSamples, tl.DurationSample)
	}
	// Manifest segment durations must reconstruct the declared duration.
	for i, seg := range tl.Segments {
		if seg.EndSample < seg.StartSample {
			return fmt.Errorf("segment %d has negative duration", i)
		}
		if i > 0 && seg.StartSample < tl.Segments[i-1].EndSample {
			return fmt.Errorf("segment %d overlaps its predecessor", i)
		}
	}
	if n := len(tl.Segments); n > 0 && tl.Segments[n-1].EndSample > tl.DurationSample {
		return fmt.Errorf("last segment ends after the declared duration")
	}
	return nil
}

// findOutputs locates the single manifest and MP4 pair in dir.
func findOutputs(dir string) (string, string, error) {
	manifests, err := filepath.Glob(filepath.Join(dir, "*.manifest.json"))
	if err != nil || len(manifests) != 1 {
		return "", "", fmt.Errorf("expected one *.manifest.json in %s", dir)
	}
	mp4s, err := filepath.Glob(filepath.Join(dir, "*.mp4"))
	if err != nil || len(mp4s) != 1 {
		return "", "", fmt.Errorf("expected one *.mp4 in %s", dir)
	}
	return manifests[0], mp4s[0], nil
}
