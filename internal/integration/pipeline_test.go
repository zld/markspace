// Package integration exercises the full pipeline end to end: fixture
// generation, parsing, repair, indexing, checkpointed recovery and the
// acceptance metrics.
package integration

import (
	"path/filepath"
	"testing"

	"mediapack/internal/batch"
	"mediapack/internal/fixture"
)

const demoInject = "gap,tfdt-backstep,truncated-box,partial-index"

func generate(t *testing.T, work string, inject ...string) batch.Options {
	t.Helper()
	cfg := fixture.Config{
		Dir: filepath.Join(work, "input"),
		Tracks: 4, Fragments: 32, Inject: inject,
	}
	if err := fixture.Generate(cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return batch.Options{WorkDir: work, ChunkSize: 8}
}

func split(list string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(list); i++ {
		if i == len(list) || list[i] == ',' {
			out = append(out, list[start:i])
			start = i + 1
		}
	}
	return out
}

func TestPipelineAcceptance(t *testing.T) {
	work := t.TempDir()
	opts := generate(t, work, split(demoInject)...)
	if err := fixture.PlantPartialIndex(work); err != nil {
		t.Fatal(err)
	}

	rep1, err := batch.Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Second run reuses the validated chunks.
	rep2, err := batch.Run(opts)
	if err != nil {
		t.Fatalf("resume Run: %v", err)
	}
	// Fresh directory, same fixtures: input-only determinism.
	fresh := filepath.Join(work, "rerun")
	freshOpts := generate(t, fresh, split(demoInject)...)
	rep3, err := batch.Run(freshOpts)
	if err != nil {
		t.Fatalf("fresh Run: %v", err)
	}

	v := &batch.Validation{}
	if err := batch.ValidateRoundtrip(opts, rep1, v); err != nil {
		t.Fatalf("ValidateRoundtrip: %v", err)
	}
	batch.ValidateKeyframes(rep1, v)

	if v.RoundtripRate != 1.0 {
		t.Errorf("roundtrip_rate=%.3f", v.RoundtripRate)
	}
	if v.KeyframeHitRate != 1.0 {
		t.Errorf("keyframe_hit_rate=%.3f", v.KeyframeHitRate)
	}
	if m, total := batch.HashMatch(rep1, rep2); m != total || total != 32 {
		t.Errorf("resume hash match %d/%d", m, total)
	}
	if m, total := batch.HashMatch(rep1, rep3); m != total || total != 32 {
		t.Errorf("fresh hash match %d/%d", m, total)
	}
	if rep1.RecoveredChunks != 1 {
		t.Errorf("recovered_chunks=%d want 1", rep1.RecoveredChunks)
	}
	if rep1.Rejected < 1 {
		t.Errorf("rejected_fragments=%d want >=1", rep1.Rejected)
	}
	if rep1.Accepted != 32 {
		t.Errorf("accepted=%d want 32", rep1.Accepted)
	}
	if err := batch.CheckInvariants(work, rep1); err != nil {
		t.Errorf("invariants: %v", err)
	}
}

// TestFaultPropagation verifies that the fault classes propagate across
// the parse, repair and index stages: parse rejects the truncated box,
// repair normalizes gap/backstep/missing-tfdt, and the index only ever
// holds monotonic keyframes of accepted fragments.
func TestFaultPropagation(t *testing.T) {
	work := t.TempDir()
	opts := generate(t, work,
		fixture.FaultGap, fixture.FaultTfdtBackstep, fixture.FaultTruncatedBox,
		fixture.FaultMissingTfdt, fixture.FaultDupMfhd)
	rep, err := batch.Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Rejected != 1 {
		t.Errorf("parse stage: rejected=%d want 1 (truncated box)", rep.Rejected)
	}
	if rep.Quarantined != 1 {
		t.Errorf("repair stage: quarantined=%d want 1 (conflicting duplicate)", rep.Quarantined)
	}
	if rep.Repaired < 2 {
		t.Errorf("repair stage: repaired=%d want >=2", rep.Repaired)
	}
	// Index stage: entries are keyframes, monotonic per track, and every
	// entry is locatable at its own timestamp.
	last := map[uint32]int64{}
	for _, e := range rep.Index.Entries() {
		if !e.Keyframe {
			t.Fatal("non-keyframe entry in index")
		}
		if prev, ok := last[e.Track]; ok && e.Time < prev {
			t.Fatalf("track %d index not monotonic", e.Track)
		}
		last[e.Track] = e.Time
		got, ok := rep.Index.Query(e.Track, e.Time)
		if !ok || got.Logical != e.Logical {
			t.Fatalf("keyframe %+v not locatable", e)
		}
	}
}

// TestRecoveryMatchesUninterrupted proves that recovering from a
// half-written index chunk yields exactly the uninterrupted result.
func TestRecoveryMatchesUninterrupted(t *testing.T) {
	clean := t.TempDir()
	cleanOpts := generate(t, clean, fixture.FaultGap, fixture.FaultTfdtBackstep)
	want, err := batch.Run(cleanOpts)
	if err != nil {
		t.Fatal(err)
	}

	crashed := t.TempDir()
	crashOpts := generate(t, crashed, fixture.FaultGap, fixture.FaultTfdtBackstep)
	if err := fixture.PlantPartialIndex(crashed); err != nil {
		t.Fatal(err)
	}
	got, err := batch.Run(crashOpts)
	if err != nil {
		t.Fatal(err)
	}
	if got.RecoveredChunks != 1 {
		t.Fatalf("recovered=%d want 1", got.RecoveredChunks)
	}
	if m, total := batch.HashMatch(want, got); m != total {
		t.Errorf("recovered run diverges: %d/%d", m, total)
	}
	// Index entry order must be identical too.
	w, g := want.Index.Entries(), got.Index.Entries()
	if len(w) != len(g) {
		t.Fatalf("index sizes differ: %d vs %d", len(w), len(g))
	}
	for i := range w {
		if w[i].Track != g[i].Track || w[i].Time != g[i].Time || w[i].Hash != g[i].Hash {
			t.Fatalf("index entry %d differs: %+v vs %+v", i, w[i], g[i])
		}
	}
}
