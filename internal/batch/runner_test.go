package batch_test

import (
	"path/filepath"
	"testing"

	"mediaflow/internal/audit"
	"mediaflow/internal/batch"
	"mediaflow/internal/fixture"
	"mediaflow/internal/probe"
)

func newTestManifest(t *testing.T, work string) *probe.Manifest {
	t.Helper()
	spec := fixture.GoodSpec("recovery")
	spec.VideoSamples = 64
	spec.KeyframeEvery = 16
	spec.IncludeAudio = false
	path := filepath.Join(work, "media", "a.mp4")
	if err := fixture.WriteAsset(path, spec); err != nil {
		t.Fatal(err)
	}
	return &probe.Manifest{
		BatchKey: "test-batch", BudgetMbps: 18, QualityMin: 0.92, ChunkSamples: 16,
		Items: []probe.Item{{
			ID: "a", Path: "media/a.mp4", Width: 1920, Height: 1080, FPS: 30,
			RequiredTracks: []probe.TrackExpectation{{ID: 1, Type: "video", Timescale: 30000}},
		}},
	}
}

func find(s *audit.Summary, id string) *audit.ItemReport {
	for _, it := range s.Items {
		if it.ItemID == id {
			return it
		}
	}
	return nil
}

func TestChunkFailureRecoveryAndIdempotentSummary(t *testing.T) {
	work := t.TempDir()
	m := newTestManifest(t, work)

	interrupted := batch.RunManifest(m, batch.Options{
		WorkDir: work,
		Hooks: batch.Hooks{AfterFirstSample: func(id string, chunk int, _ string) error {
			if id == "a" && chunk == 1 {
				return assertInjected()
			}
			return nil
		}},
	})
	failed := find(interrupted, "a")
	if failed == nil || failed.Status != audit.StatusChunkFailed {
		t.Fatalf("expected chunk failure, got %+v", failed)
	}

	recovered := batch.RunManifest(m, batch.Options{WorkDir: work})
	ok := find(recovered, "a")
	if ok == nil || ok.Status != audit.StatusPass {
		t.Fatalf("expected recovered pass, got %+v", ok)
	}
	if len(ok.RolledBackChunks) != 1 || ok.RolledBackChunks[0] != 1 {
		t.Fatalf("rollback = %v, want [1]", ok.RolledBackChunks)
	}
	if len(ok.SkippedChunks) == 0 || ok.SkippedChunks[0] != 0 {
		t.Fatalf("skipped = %v, want chunk 0", ok.SkippedChunks)
	}
	if ok.OutputDigest == "" || !ok.BitrateWithinTol {
		t.Fatalf("recovery output invalid: %+v", ok)
	}
	stable1, err := audit.StableDigest(recovered)
	if err != nil {
		t.Fatal(err)
	}

	repeated := batch.RunManifest(m, batch.Options{WorkDir: work})
	again := find(repeated, "a")
	if again == nil || again.Status != audit.StatusPass || again.OutputDigest != ok.OutputDigest {
		t.Fatalf("idempotent rerun changed output: %+v vs %+v", again, ok)
	}
	stable2, err := audit.StableDigest(repeated)
	if err != nil {
		t.Fatal(err)
	}
	if stable1 != stable2 {
		t.Fatalf("stable summaries differ:\n%s\n%s", stable1, stable2)
	}
}

func TestChangedConfigRejectsCheckpoint(t *testing.T) {
	work := t.TempDir()
	m := newTestManifest(t, work)
	first := batch.RunManifest(m, batch.Options{WorkDir: work})
	if find(first, "a").Status != audit.StatusPass {
		t.Fatalf("setup did not pass: %+v", find(first, "a"))
	}

	changed := *m
	changed.QualityMin = 0.90
	conflict := batch.RunManifest(&changed, batch.Options{WorkDir: work})
	rep := find(conflict, "a")
	if rep == nil || rep.Status != audit.StatusCheckpointConflict {
		t.Fatalf("expected checkpoint conflict, got %+v", rep)
	}
	if len(rep.Reasons) == 0 {
		t.Fatal("conflict report must explain the changed identity")
	}
}

func TestDuplicateIDsDoNotHideOtherItems(t *testing.T) {
	work := t.TempDir()
	m := newTestManifest(t, work)
	goodPath := filepath.Join(work, "media", "b.mp4")
	spec := fixture.GoodSpec("b")
	spec.VideoSamples = 32
	spec.KeyframeEvery = 16
	spec.IncludeAudio = false
	if err := fixture.WriteAsset(goodPath, spec); err != nil {
		t.Fatal(err)
	}
	m.Items = append(m.Items,
		probe.Item{ID: "dup", Path: "media/a.mp4"},
		probe.Item{ID: "dup", Path: "media/a.mp4"},
		probe.Item{ID: "b", Path: "media/b.mp4", Width: 1920, Height: 1080, FPS: 30},
	)
	s := batch.RunManifest(m, batch.Options{WorkDir: work})
	if len(s.Items) != 4 {
		t.Fatalf("expected all four conclusions, got %d", len(s.Items))
	}
	dupCount := 0
	for _, it := range s.Items {
		if it.ItemID == "dup" && it.Status == audit.StatusManifestError {
			dupCount++
		}
	}
	if dupCount != 2 {
		t.Fatalf("expected two duplicate item errors, got %d", dupCount)
	}
	if b := find(s, "b"); b == nil || b.Status != audit.StatusPass {
		t.Fatalf("unique item should still process: %+v", b)
	}
}

type injectedError struct{}

func (injectedError) Error() string { return "injected test interruption" }

func assertInjected() error { return injectedError{} }
