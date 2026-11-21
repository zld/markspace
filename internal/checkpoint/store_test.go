package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bitrack/internal/model"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cp := &model.Checkpoint{
		Version: 1, JobID: "j1", InputSHA256: "abc", ParamsDigest: "dig",
		Segments: []*model.Segment{{Index: 0, State: model.StateCommitted, TargetBits: 1234}},
	}
	if err := store.Save(cp); err != nil {
		t.Fatal(err)
	}
	// No temp file left behind after atomic rename.
	if _, err := os.Stat(store.Path() + tmpSuffix); !os.IsNotExist(err) {
		t.Errorf("temp file should be removed after save")
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.JobID != "j1" || got.Segments[0].TargetBits != 1234 {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestSaveRejectsMissingDir(t *testing.T) {
	if _, err := NewStore(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected error for nonexistent workdir")
	}
}

func TestVerifyCommitted(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "seg.mp4")
	if err := os.WriteFile(out, []byte("encoded-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := FileSHA256(out)
	if err != nil {
		t.Fatal(err)
	}
	good := &model.Segment{Index: 0, State: model.StateCommitted, OutputPath: out, Checksum: sum}
	if d := VerifyCommitted(good); !d.Reusable {
		t.Errorf("valid segment rejected: %s", d.Reason)
	}
	t.Run("tampered", func(t *testing.T) {
		if err := os.WriteFile(out, []byte("changed-bytes-here"), 0o644); err != nil {
			t.Fatal(err)
		}
		d := VerifyCommitted(good)
		if d.Reusable || !strings.Contains(d.Reason, "checksum mismatch") {
			t.Errorf("expected checksum mismatch, got reusable=%v reason=%q", d.Reusable, d.Reason)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		empty := filepath.Join(dir, "empty.mp4")
		_ = os.WriteFile(empty, nil, 0o644)
		d := VerifyCommitted(&model.Segment{Index: 1, State: model.StateCommitted, OutputPath: empty, Checksum: "x"})
		if d.Reusable || !strings.Contains(d.Reason, "empty") {
			t.Errorf("expected empty file rejection, got %q", d.Reason)
		}
	})
	t.Run("missing", func(t *testing.T) {
		d := VerifyCommitted(&model.Segment{Index: 2, State: model.StateCommitted, OutputPath: filepath.Join(dir, "nope.mp4"), Checksum: "x"})
		if d.Reusable || !strings.Contains(d.Reason, "missing") {
			t.Errorf("expected missing file rejection, got %q", d.Reason)
		}
	})
}

func TestDigestStabilityAndDistinction(t *testing.T) {
	p := model.Params{TargetKbps: 300, Width: 176, Height: 144, Encoder: "libx264", Preset: "medium"}
	d1, err := DigestParams(p)
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := DigestParams(p)
	if d1 != d2 {
		t.Error("digest must be deterministic")
	}
	changed := p
	changed.Preset = "slow"
	d3, _ := DigestParams(changed)
	if d1 == d3 {
		t.Error("parameter change must produce a different digest")
	}
	b1, _ := DigestBaseline(p)
	b2, _ := DigestBaseline(changed)
	if b1 == b2 {
		t.Error("baseline digest must change when encoder settings change")
	}
	// Different target kbps shares the bitrate-independent baseline digest.
	hi := p
	hi.TargetKbps = 900
	b3, _ := DigestBaseline(hi)
	if b1 != b3 {
		t.Error("baseline digest must ignore target bitrate")
	}
}
