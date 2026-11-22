package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/archive/mediakernel/internal/timeline"
)

func goodAsset() *Asset {
	return &Asset{
		ID: "a1", MP4Path: "media/a.mp4", RTPPath: "rtp/a.rtp",
		Timescale: 90000, ClockRate: 48000, TargetBitrate: 2_400_000,
	}
}

func TestValidateAcceptsWellFormed(t *testing.T) {
	m := &Manifest{Version: 1, Assets: []*Asset{goodAsset()}}
	if err := m.Validate(t.TempDir()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsBadFields(t *testing.T) {
	bad := func(mutate func(*Asset)) error {
		a := goodAsset()
		mutate(a)
		m := &Manifest{Version: 1, Assets: []*Asset{a}}
		return m.Validate(t.TempDir())
	}
	if Classify(bad(func(a *Asset) { a.Timescale = 0 })) != timeline.KindInvalidInput {
		t.Fatal("zero timescale must be KindInvalidInput")
	}
	if Classify(bad(func(a *Asset) { a.ClockRate = 0 })) != timeline.KindInvalidInput {
		t.Fatal("zero clock rate must be KindInvalidInput")
	}
	if Classify(bad(func(a *Asset) { a.TargetBitrate = -1 })) != timeline.KindInvalidInput {
		t.Fatal("negative bitrate must be KindInvalidInput")
	}
	if Classify(bad(func(a *Asset) { a.ID = "" })) != timeline.KindInvalidInput {
		t.Fatal("empty id must be KindInvalidInput")
	}
	if Classify(bad(func(a *Asset) {
		a.Edits = []EditEntry{{MediaStartUS: -1, DurationUS: 1}}
	})) != timeline.KindInvalidInput {
		t.Fatal("edit before start offset must be rejected")
	}
	dup := &Manifest{Version: 1, Assets: []*Asset{goodAsset(), goodAsset()}}
	if Classify(dup.Validate(t.TempDir())) != timeline.KindInvalidInput {
		t.Fatal("duplicate asset id must be rejected")
	}
	wrongVer := &Manifest{Version: 9, Assets: []*Asset{goodAsset()}}
	if Classify(wrongVer.Validate(t.TempDir())) != timeline.KindInvalidInput {
		t.Fatal("unsupported version must be rejected")
	}
}

func Classify(err error) timeline.Kind { return timeline.Classify(err) }

func TestSafePathRejectsTraversalAndAbsolute(t *testing.T) {
	base := t.TempDir()
	for _, p := range []string{"../escape.mp4", "../../etc/passwd", "/etc/passwd"} {
		if _, err := SafePath(p, base); Classify(err) != timeline.KindInvalidInput {
			t.Fatalf("path %q accepted: %v", p, err)
		}
	}
	if _, err := SafePath("nested/../a.mp4", base); err != nil {
		t.Fatalf("in-base relative path rejected: %v", err)
	}
}

func TestSafePathRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.mp4")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(base, "link.mp4")); err != nil {
		t.Fatal(err)
	}
	if _, err := SafePath("link.mp4", base); Classify(err) != timeline.KindInvalidInput {
		t.Fatalf("symlink escape accepted: %v", err)
	}
}

func TestWriteFileRoundTripAndRejectsUnsorted(t *testing.T) {
	base := t.TempDir()
	good := &ResultManifest{
		SchemaVersion: 1, InputDigest: "sha256:abc",
		Segments: []Segment{{Index: 0, StartUS: 0, EndUS: 1000, KeyframeUS: 0,
			BoundaryDevUS: 0, Fingerprint: "h1", SourceAsset: "a1"}},
	}
	if err := WriteFile("out/result.json", base, good); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "out", "result.json")); err != nil {
		t.Fatalf("output missing: %v", err)
	}
	bad := &ResultManifest{SchemaVersion: 1, InputDigest: "x",
		Segments: []Segment{
			{Index: 0, StartUS: 2000, EndUS: 3000},
			{Index: 1, StartUS: 1000, EndUS: 2000},
		}}
	if Classify(WriteFile("bad.json", base, bad)) != timeline.KindInternal {
		t.Fatal("unsorted segments must fail validation")
	}
}
