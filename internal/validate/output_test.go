package validate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/model"
)

func writeValidOutput(t *testing.T, root string) *Manifest {
	t.Helper()
	frames := []model.OutputFrame{
		{Number: 1, Track: 1, Sequence: 1, CaptureNanos: 20_000_000, SampleRate: 48000,
			Channels: model.LayoutMono, TargetLUFS: -23, MeasuredLUFS: -23, Payload: []byte("aaaa")},
		{Number: 2, Track: 1, Sequence: 2, CaptureNanos: 40_000_000, SampleRate: 48000,
			Channels: model.LayoutMono, TargetLUFS: -23, MeasuredLUFS: -23, Payload: []byte("bbbb")},
	}
	for i := range frames {
		frames[i].PayloadLength = uint32(len(frames[i].Payload))
		frames[i].SHA256 = model.Checksum(frames[i].Payload)
	}
	m, err := WriteOutput(root, frames, 48000, model.LayoutMono, -23)
	if err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}
	return m
}

func TestVerifyAcceptsValidOutput(t *testing.T) {
	root := t.TempDir()
	writeValidOutput(t, root)
	report, _, err := Verify(root, 2, 48000, model.LayoutMono, -23)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Status != "validated" || report.Frames != 2 {
		t.Fatalf("report = %+v", report)
	}
}

func TestVerifyFailsOnTamperedContainer(t *testing.T) {
	root := t.TempDir()
	writeValidOutput(t, root)
	path, err := model.SafeJoin(root, filepath.Join(OutputDir, ContainerName))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF // flip a payload byte so the on-disk hash no longer matches
	if err = os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	report, _, err := Verify(root, 2, 48000, model.LayoutMono, -23)
	if err == nil {
		t.Fatal("tampered container was accepted")
	}
	if report == nil || report.Status != "failed" {
		t.Fatalf("report = %+v, want failed", report)
	}
}

func TestVerifyFailsOnLoudnessDrift(t *testing.T) {
	root := t.TempDir()
	m := writeValidOutput(t, root)
	// Re-write the manifest claiming an out-of-tolerance measured loudness,
	// then recompute its self-hash so the loudness check is what fails rather
	// than the earlier manifest self-hash guard.
	m.Frames[0].MeasuredLUFS = -10
	selfHash, err := model.CanonicalHash(bodyOf(m))
	if err != nil {
		t.Fatal(err)
	}
	m.ManifestSHA256 = selfHash
	doc, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path, err := model.SafeJoin(root, filepath.Join(OutputDir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err = Verify(root, 2, 48000, model.LayoutMono, -23); err == nil {
		t.Fatal("loudness drift beyond tolerance was accepted")
	}
}

func TestVerifyFailsOnFrameCountMismatch(t *testing.T) {
	root := t.TempDir()
	writeValidOutput(t, root)
	if _, _, err := Verify(root, 3, 48000, model.LayoutMono, -23); err == nil {
		t.Fatal("frame count mismatch accepted")
	}
}
