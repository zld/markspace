// Package sample tests check that the fixtures promise exactly what the
// acceptance run asserts about them.
package sample

import (
	"testing"

	"mediarc/container"
	"mediarc/subtitle"
)

func TestSRTFixtures(t *testing.T) {
	good := BuildSRT(false)
	if good.InvalidCues != 0 {
		t.Fatalf("clean fixture has invalid cues")
	}
	parsed := subtitle.ParseSRT(good.Data)
	if len(parsed.Issues) != 0 {
		t.Fatalf("clean fixture issues: %v", parsed.Issues)
	}
	if len(parsed.Cues) != good.ValidCues || len(parsed.Cues) != 4 {
		t.Fatalf("clean fixture cues = %d, want %d", len(parsed.Cues), good.ValidCues)
	}
	// Cue 1 runs from 1 s to 3 s at 25 fps.
	if parsed.Cues[0].Start != 1_000_000_000 || parsed.Cues[0].End != 3_000_000_000 {
		t.Fatalf("first cue = %d..%d", parsed.Cues[0].Start, parsed.Cues[0].End)
	}

	corrupt := BuildSRT(true)
	parsedCorrupt := subtitle.ParseSRT(corrupt.Data)
	if len(parsedCorrupt.Cues) != corrupt.ValidCues {
		t.Fatalf("corrupt fixture recovered %d cues, want %d", len(parsedCorrupt.Cues), corrupt.ValidCues)
	}
	if len(parsedCorrupt.Issues) != corrupt.InvalidCues {
		t.Fatalf("corrupt fixture issues = %d, want %d", len(parsedCorrupt.Issues), corrupt.InvalidCues)
	}
}

func TestVTTFixture(t *testing.T) {
	fx := BuildVTT()
	parsed := subtitle.ParseWebVTT(fx.Data)
	if len(parsed.Issues) != 0 {
		t.Fatalf("issues = %v", parsed.Issues)
	}
	if len(parsed.Cues) != fx.ValidCues {
		t.Fatalf("cues = %d, want %d", len(parsed.Cues), fx.ValidCues)
	}
	if parsed.Cues[0].Text() != "Opening credits" {
		t.Fatalf("first cue = %q", parsed.Cues[0].Text())
	}
}

func TestMP4Fixtures(t *testing.T) {
	dir := t.TempDir()
	good, err := BuildMP4(dir, "good.mp4", MP4Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := container.ScanFile(good.Path, container.ScanOptions{})
	if err != nil {
		t.Fatalf("scan good: %v", err)
	}
	if !result.IndexComplete {
		t.Fatalf("good fixture index incomplete: %+v", result.Divergences)
	}
	if result.IndexedSampleCount() != good.IntactSamples {
		t.Fatalf("good fixture samples = %d, want %d", result.IndexedSampleCount(), good.IntactSamples)
	}
}

func TestMP4TruncatedFixture(t *testing.T) {
	dir := t.TempDir()
	truncated, err := BuildMP4(dir, "truncated.mp4", MP4Options{TruncateBytes: 9000})
	if err != nil {
		t.Fatal(err)
	}
	if truncated.FirstUnreadableHint >= truncated.Size || truncated.FirstUnreadableHint < 0 {
		t.Fatalf("hint %d not inside file of %d bytes", truncated.FirstUnreadableHint, truncated.Size)
	}
	result, err := container.ScanFile(truncated.Path, container.ScanOptions{})
	if err != nil {
		t.Fatalf("scan truncated: %v", err)
	}
	if !result.RecoveredWithinFile() {
		t.Fatalf("first unreadable %d outside %d", result.FirstUnreadable, result.Size)
	}
	if result.IndexedSampleCount() != truncated.ExpectedRecovered {
		t.Fatalf("index recovered %d samples, want %d", result.IndexedSampleCount(), truncated.ExpectedRecovered)
	}
	if result.RecoveredSamples == 0 {
		t.Fatal("expected boundary recovery for the mdat tail")
	}
	if result.SampleCount() != truncated.ExpectedRecovered+result.RecoveredSamples {
		t.Fatalf("sample total %d does not add up", result.SampleCount())
	}
}

func TestMP4BrokenIndexFixture(t *testing.T) {
	dir := t.TempDir()
	broken, err := BuildMP4(dir, "broken-index.mp4", MP4Options{IndexShiftWidens: 5_000_000})
	if err != nil {
		t.Fatal(err)
	}
	result, err := container.ScanFile(broken.Path, container.ScanOptions{})
	if err != nil {
		t.Fatalf("scan broken: %v", err)
	}
	if result.IndexComplete {
		t.Fatal("broken index should be incomplete")
	}
	if !result.RecoveredWithinFile() {
		t.Fatalf("first unreadable %d outside %d", result.FirstUnreadable, result.Size)
	}
	if result.RecoveredSamples == 0 {
		t.Fatal("expected boundary recovery")
	}
}

func TestMP4MissingIndexFixture(t *testing.T) {
	dir := t.TempDir()
	missing, err := BuildMP4(dir, "no-index.mp4", MP4Options{OmitIndex: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := container.ScanFile(missing.Path, container.ScanOptions{})
	if err != nil {
		t.Fatalf("scan no-index: %v", err)
	}
	if result.IndexComplete {
		t.Fatal("missing index should be incomplete")
	}
	if result.RecoveredSamples == 0 {
		t.Fatal("expected boundary recovery")
	}
	if !result.RecoveredWithinFile() {
		t.Fatalf("first unreadable %d outside %d", result.FirstUnreadable, result.Size)
	}
}
