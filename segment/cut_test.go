package segment_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"continuity/fixture"
	"continuity/manifest"
	"continuity/media"
	"continuity/planner"
	"continuity/segment"
	"continuity/worker"
)

type pipeline struct {
	dir    string
	inputs []*media.Container
	plan   *planner.Plan
}

func setup(t *testing.T, specs ...fixture.Spec) *pipeline {
	t.Helper()
	dir := t.TempDir()
	p := &pipeline{dir: dir}
	for i, s := range specs {
		path := filepath.Join(dir, "in"+string(rune('a'+i))+".mp4")
		if err := fixture.Generate(path, s); err != nil {
			t.Fatal(err)
		}
		c, err := media.Probe(path)
		if err != nil {
			t.Fatal(err)
		}
		p.inputs = append(p.inputs, c)
	}
	plan, _, err := planner.Evaluate(p.inputs, 2_000_000_000, planner.Constraints{
		MaxBufferBytes: 1 << 20, MaxConcurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	p.plan = plan
	return p
}

func (p *pipeline) cut(t *testing.T) *segment.Result {
	t.Helper()
	out := filepath.Join(p.dir, "out")
	res, err := segment.Cut(context.Background(), p.inputs, p.plan, out, &worker.Pool{Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCutValidAndReproducible(t *testing.T) {
	p := setup(t, fixture.ValidSpec(), fixture.ValidSpec())
	res := p.cut(t)
	if res.Recomputed != 6 || res.Reused != 0 {
		t.Fatalf("first run: recomputed=%d reused=%d, want 6/0", res.Recomputed, res.Reused)
	}
	if res.HitRate != 1.0 {
		t.Fatalf("hit rate %.3f != 1.000", res.HitRate)
	}
	m1 := manifest.Build(p.inputs, p.plan, res)
	h1, err := manifest.Hash(m1)
	if err != nil {
		t.Fatal(err)
	}

	// Second run over the same state: everything reused, same hashes.
	res2 := p.cut(t)
	if res2.Reused != 6 || res2.Recomputed != 0 {
		t.Fatalf("second run: reused=%d recomputed=%d, want 6/0", res2.Reused, res2.Recomputed)
	}
	m2 := manifest.Build(p.inputs, p.plan, res2)
	h2, err := manifest.Hash(m2)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("manifest hash changed across runs: %s != %s", h1, h2)
	}
	for _, s := range res2.Segments {
		if len(s.SHA256) != 64 {
			t.Fatalf("segment hash %q not SHA-256 hex", s.SHA256)
		}
	}
}

func TestCutResumeRecomputesCorrupted(t *testing.T) {
	p := setup(t, fixture.ValidSpec())
	res := p.cut(t)
	if res.Recomputed != 3 {
		t.Fatalf("recomputed=%d, want 3", res.Recomputed)
	}
	// Corrupt one artifact on disk; the recorded state hash no longer
	// matches, so exactly that segment must be recomputed.
	victim := filepath.Join(p.dir, "out", res.Segments[1].FileName())
	if err := os.WriteFile(victim, []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	res2 := p.cut(t)
	if res2.Reused != 2 || res2.Recomputed != 1 {
		t.Fatalf("resume: reused=%d recomputed=%d, want 2/1", res2.Reused, res2.Recomputed)
	}
	if res2.Segments[1].SHA256 != res.Segments[1].SHA256 {
		t.Fatal("recomputed segment hash differs from original deterministic hash")
	}
}

func TestCutMissingKeyframeFails(t *testing.T) {
	p := setup(t, fixture.ValidSpec(), fixture.MissingKeyframeSpec())
	out := filepath.Join(p.dir, "out")
	_, err := segment.Cut(context.Background(), p.inputs, p.plan, out, &worker.Pool{Size: 4})
	if !media.IsKind(err, media.KindKeyframeMiss) {
		t.Fatalf("want keyframe-miss, got %v", err)
	}
	// No partial manifest may exist.
	if _, statErr := os.Stat(filepath.Join(out, manifest.FileName)); !os.IsNotExist(statErr) {
		t.Fatal("partial manifest published after keyframe miss")
	}
}
