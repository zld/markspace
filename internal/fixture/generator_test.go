package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"mediapipe/internal/manifest"
	"mediapipe/internal/verify"
)

func TestBuildHealthyFixture(t *testing.T) {
	dir := t.TempDir()
	gen, err := Build(dir, Spec{Seed: 17, Frames: 24, AudioRate: 44100, Channels: 6})
	if err != nil {
		t.Fatal(err)
	}
	if gen.CorruptAt != -1 {
		t.Fatal("clean fixture must report no corruption point")
	}
	// Every declared frame exists on disk.
	for _, tr := range gen.Manifest.Tracks {
		for _, f := range tr.Frames {
			base := filepath.Join(dir, tr.ID)
			if _, err := os.Stat(filepath.Join(base, f.Name)); err != nil {
				t.Fatalf("missing payload %s: %v", f.Name, err)
			}
		}
	}
	// The generated manifest must parse through the real manifest package.
	tracks, err := manifest.ParseFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	res := verify.All(tracks, verify.FSFrameOpener(dir))
	for _, rep := range res.Reports {
		if !rep.OK {
			t.Fatalf("healthy fixture failed verification: %+v", rep.FirstError)
		}
	}
	if res.CorruptFirstFrame != -1 {
		t.Fatalf("healthy fixture reported corruption: %+v", res)
	}
}

func TestCorruptionInjectors(t *testing.T) {
	cases := []struct {
		kind Corruption
		want string
	}{
		{Truncated, "truncated_frame"},
		{HashMismatch, "hash_mismatch"},
		{TimestampBack, "timestamp_regression"},
		{UnknownColor, ""}, // verification passes; color stage must block later
	}
	for _, c := range cases {
		dir := t.TempDir()
		gen, err := Build(dir, Spec{Seed: 17, Frames: 30, AudioRate: 48000,
			Channels: 2, Corruption: c.kind})
		if err != nil {
			t.Fatal(err)
		}
		tracks, err := manifest.ParseFile(filepath.Join(dir, "manifest.json"))
		if c.kind == TimestampBack {
			if err == nil {
				t.Fatalf("%s: parse should reject regression", c.kind)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: parse error: %v", c.kind, err)
		}
		res := verify.All(tracks, verify.FSFrameOpener(dir))
		switch c.want {
		case "":
			if res.CorruptFirstFrame != -1 {
				t.Fatalf("%s: verification should pass, got %+v", c.kind, res)
			}
		default:
			if res.CorruptFirstFrame != gen.CorruptAt {
				t.Fatalf("%s: located frame %d, want %d", c.kind, res.CorruptFirstFrame, gen.CorruptAt)
			}
			// Find the failing report by track id; tracks are id-sorted, so
			// the video report is not necessarily index 0.
			var failing *verify.Report
			for i := range res.Reports {
				if !res.Reports[i].OK {
					failing = &res.Reports[i]
				}
			}
			if failing == nil || string(failing.FirstError.Type) != c.want {
				t.Fatalf("%s: error classification wrong: %+v", c.kind, failing)
			}
		}
	}
}

func TestSeedDeterminism(t *testing.T) {
	a, err := Build(t.TempDir(), Spec{Seed: 17, Frames: 12, AudioRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(t.TempDir(), Spec{Seed: 17, Frames: 12, AudioRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.AudioTrack.PCM) != len(b.AudioTrack.PCM) {
		t.Fatal("fixture sizes differ")
	}
	for i := range a.AudioTrack.PCM {
		if a.AudioTrack.PCM[i] != b.AudioTrack.PCM[i] {
			t.Fatal("same seed produced different audio")
		}
	}
}
