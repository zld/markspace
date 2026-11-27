package cache_test

import (
	"path/filepath"
	"testing"

	"continuity/internal/cache"
	"continuity/internal/fixture"
	"continuity/internal/index"
	"continuity/internal/manifest"
	"continuity/internal/model"
)

func generateDataset(t *testing.T, root string, frames int) ([]model.Frame, fixture.Spec) {
	t.Helper()
	spec := fixture.DefaultSpec(frames)
	gen, err := fixture.Generate(root, spec)
	if err != nil {
		t.Fatalf("fixture.Generate: %v", err)
	}
	return gen, spec
}

func buildIndex(t *testing.T, root string, frames []model.Frame, metaVersion uint32) *index.Index {
	t.Helper()
	idx, err := index.Build(root, fixture.ContainerName, metaVersion, frames)
	if err != nil {
		t.Fatalf("index.Build: %v", err)
	}
	return idx
}

func TestIndexLocatesEveryFrameAndPayloadVerifies(t *testing.T) {
	root := t.TempDir()
	_, spec := generateDataset(t, root, 6)

	parsed, err := manifest.Read(root)
	if err != nil {
		t.Fatalf("manifest.Read: %v", err)
	}
	idx := buildIndex(t, root, parsed, spec.MetadataRev)
	if len(idx.Positions) != 6 {
		t.Fatalf("positions = %d, want 6", len(idx.Positions))
	}
	// Offsets must be strictly increasing and point inside the container.
	for i, p := range idx.Positions {
		if p.Number != model.FrameNumber(i+1) {
			t.Fatalf("position %d number = %d", i, p.Number)
		}
		if i > 0 && p.Offset <= idx.Positions[i-1].Offset {
			t.Fatalf("offset not monotonic at %d: %d <= %d", i, p.Offset, idx.Positions[i-1].Offset)
		}
		if err = manifest.VerifyPayload(&parsed[i]); err != nil {
			t.Fatalf("frame %d payload verification: %v", i+1, err)
		}
	}
}

func TestTruncatedContainerReportsFrameThree(t *testing.T) {
	root := t.TempDir()
	frames, spec := generateDataset(t, root, 12)
	if _, err := fixture.Mutate(root, spec, frames, fixture.MutateTruncate); err != nil {
		t.Fatalf("mutate truncate: %v", err)
	}

	parsed, err := manifest.Read(root)
	if err != nil {
		t.Fatalf("manifest.Read: %v", err)
	}
	_, err = index.Build(root, fixture.ContainerName, spec.MetadataRev, parsed)
	se, ok := model.AsStructured(err)
	if !ok {
		t.Fatalf("expected structured error, got %v", err)
	}
	if se.Stage != model.StageManifest || se.Frame != 3 || se.Offset < 0 {
		t.Fatalf("error anchor = stage=%s frame=%d offset=%d, want manifest/3/non-negative",
			se.Stage, se.Frame, se.Offset)
	}
}

func TestFingerprintInvalidationClasses(t *testing.T) {
	root := t.TempDir()
	_, spec := generateDataset(t, root, 4)

	// First observation: fresh, no invalidations.
	store, err := cache.LoadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	var counters cache.Counters
	fp := cache.Fingerprint{
		Path:             filepath.Join(fixture.InputDir, fixture.ContainerName),
		ContentSHA256:    "content-v1",
		ContainerVersion: spec.ContainerRev,
		MetadataVersion:  spec.MetadataRev,
	}
	d := store.Evaluate(fp, &counters)
	if d.Reason != cache.ReasonFresh || counters.ContentInvalidations != 0 || counters.IndexInvalidations != 0 {
		t.Fatalf("fresh evaluation wrong: %+v counters=%+v", d, counters)
	}

	// Metadata-only bump: content cache valid, index invalidated.
	metaFP := fp
	metaFP.MetadataVersion++
	d = store.Evaluate(metaFP, &counters)
	if d.Reason != cache.ReasonMetadataChanged || !d.ContentCacheValid || d.IndexValid {
		t.Fatalf("metadata mutation decision wrong: %+v", d)
	}
	if counters.ContentInvalidations != 0 || counters.IndexInvalidations != 1 {
		t.Fatalf("metadata counters wrong: %+v", counters)
	}

	// Container bump: same content, index invalidated again.
	contFP := metaFP
	contFP.ContainerVersion++
	d = store.Evaluate(contFP, &counters)
	if d.Reason != cache.ReasonContainerChanged || !d.ContentCacheValid || d.IndexValid {
		t.Fatalf("container mutation decision wrong: %+v", d)
	}
	if counters.ContentInvalidations != 0 || counters.IndexInvalidations != 2 {
		t.Fatalf("container counters wrong: %+v", counters)
	}

	// Content replacement: content cache invalidated, index rebuild implied but
	// not double-counted as an index invalidation.
	replFP := contFP
	replFP.ContentSHA256 = "content-v2"
	d = store.Evaluate(replFP, &counters)
	if d.Reason != cache.ReasonContentReplaced || d.ContentCacheValid || d.IndexValid {
		t.Fatalf("replace mutation decision wrong: %+v", d)
	}
	if counters.ContentInvalidations != 1 || counters.IndexInvalidations != 2 {
		t.Fatalf("replace counters wrong: %+v", counters)
	}

	// Store must survive an atomic save/load round trip.
	if err = store.Save(root); err != nil {
		t.Fatalf("save store: %v", err)
	}
	reloaded, err := cache.LoadStore(root)
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if got := reloaded.Entries[fp.Path].ContentSHA256; got != "content-v2" {
		t.Fatalf("persisted content hash = %q, want content-v2", got)
	}
}

func TestReplaceChangesContentHashAndReindexSucceeds(t *testing.T) {
	root := t.TempDir()
	frames, spec := generateDataset(t, root, 5)
	replaced, err := fixture.Mutate(root, spec, frames, fixture.MutateReplace)
	if err != nil {
		t.Fatalf("mutate replace: %v", err)
	}
	parsed, err := manifest.Read(root)
	if err != nil {
		t.Fatalf("manifest.Read after replace: %v", err)
	}
	idx := buildIndex(t, root, parsed, spec.MetadataRev)
	if len(idx.Positions) != len(replaced) {
		t.Fatalf("rebuilt index size = %d", len(idx.Positions))
	}
	for i := range parsed {
		if err = manifest.VerifyPayload(&parsed[i]); err != nil {
			t.Fatalf("replaced frame %d verification: %v", i+1, err)
		}
	}
}
