package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/media-check/media"
)

func TestRunResumeReusesVerifiedArtifacts(t *testing.T) {
	dir := t.TempDir()
	src := writeFixture(t, dir)
	out := filepath.Join(dir, "run")
	first, err := Run(context.Background(), runOpts(src, out))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range first.Map.Entries {
		if e.Resumed {
			t.Fatal("first run reported resumed entries")
		}
	}
	second, err := Run(context.Background(), runOpts(src, out))
	if err != nil {
		t.Fatal(err)
	}
	if second.Map.SHA256 != first.Map.SHA256 {
		t.Fatalf("resume changed the digest: %s != %s", second.Map.SHA256, first.Map.SHA256)
	}
	resumed := 0
	for _, e := range second.Map.Entries {
		if e.Resumed {
			resumed++
		}
	}
	if resumed != len(second.Map.Entries) {
		t.Errorf("resumed %d of %d entries; all should be reused", resumed, len(second.Map.Entries))
	}
}

func TestRunRecomputesOnTruncatedArtifact(t *testing.T) {
	dir := t.TempDir()
	src := writeFixture(t, dir)
	out := filepath.Join(dir, "run")
	if _, err := Run(context.Background(), runOpts(src, out)); err != nil {
		t.Fatal(err)
	}
	// Corrupt one artifact file; resume must detect the inconsistency and
	// recompute rather than trust it.
	art := filepath.Join(out, "artifacts", "seg-0002.json")
	raw, err := os.ReadFile(art)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(art, raw[:len(raw)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), runOpts(src, out))
	if err != nil {
		t.Fatalf("run after artifact truncation: %v", err)
	}
	for _, e := range second.Map.Entries {
		if e.Index == 2 && e.Resumed {
			t.Fatal("truncated artifact was reused")
		}
	}
	if !second.Map.Complete {
		t.Error("recompute run incomplete")
	}
}

func TestRunRecomputesWhenInputChanges(t *testing.T) {
	dir := t.TempDir()
	src := writeFixture(t, dir)
	out := filepath.Join(dir, "run")
	first, err := Run(context.Background(), runOpts(src, out))
	if err != nil {
		t.Fatal(err)
	}
	// Perturb one sample byte in the mdat; the source digest changes, so every
	// artifact must be recomputed and at least one segment hash must differ.
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := media.ProbeFile(bytes.NewReader(raw), int64(len(raw)), src)
	if err != nil {
		t.Fatal(err)
	}
	victim := probe.Tracks[0].Table.Samples[3]
	raw[victim.Offset] ^= 0xff
	if err := os.WriteFile(src, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), runOpts(src, out))
	if err != nil {
		t.Fatalf("run after input change: %v", err)
	}
	for _, e := range second.Map.Entries {
		if e.Resumed {
			t.Fatalf("entry %d was resumed after the input changed", e.Index)
		}
	}
	changed := false
	for i := range first.Map.Entries {
		if first.Map.Entries[i].SHA256 != second.Map.Entries[i].SHA256 {
			changed = true
		}
	}
	if !changed {
		t.Error("input change did not alter any segment hash")
	}
	if second.Map.SHA256 == first.Map.SHA256 {
		t.Error("manifest digest unchanged after input changed")
	}
}
