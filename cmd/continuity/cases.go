package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"continuity/fixture"
	"continuity/manifest"
	"continuity/media"
	"continuity/planner"
	"continuity/segment"
	"continuity/worker"
)

// caseSpecs assembles the input specs for a demo case.
func caseSpecs(name string, reps int) ([]fixture.Spec, error) {
	mk := func(mut func(*fixture.Spec)) []fixture.Spec {
		var specs []fixture.Spec
		for i := 0; i < reps; i++ {
			s := fixture.ValidSpec()
			if mut != nil && i == reps-1 {
				mut(&s)
			}
			specs = append(specs, s)
		}
		return specs
	}
	switch name {
	case "valid", "resume":
		return mk(nil), nil
	case "corrupt-box":
		return mk(func(s *fixture.Spec) { s.TruncateMoov = true }), nil
	case "missing-keyframe":
		if reps < 2 {
			return nil, fmt.Errorf("missing-keyframe needs --representations >= 2")
		}
		return mk(func(s *fixture.Spec) { *s = fixture.MissingKeyframeSpec() }), nil
	case "audio-conflict":
		return mk(func(s *fixture.Spec) { *s = fixture.AudioConflictSpec() }), nil
	case "color-conflict":
		return mk(func(s *fixture.Spec) { *s = fixture.ColorConflictSpec() }), nil
	default:
		return nil, fmt.Errorf("unknown case %q", name)
	}
}

// runResume executes the interrupted-run scenario: a full run, one
// corrupted artifact, then a resumed run that must reuse the intact
// segments, recompute the corrupted one and reproduce the same manifest
// hash.
func runResume(inputs []*media.Container, plan *planner.Plan, outDir, workdir string,
	pool *worker.Pool, stdout, stderr io.Writer) int {

	ctx := context.Background()
	res1, err := segment.Cut(ctx, inputs, plan, outDir, pool)
	if err != nil {
		return fail(stderr, err)
	}
	m1 := manifest.Build(inputs, plan, res1)
	h1, err := manifest.Hash(m1)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// Simulate an interrupted run leaving a corrupted artifact behind.
	victim := filepath.Join(outDir, res1.Segments[0].FileName())
	if err := os.WriteFile(victim, []byte("torn-write"), 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	res2, err := segment.Cut(ctx, inputs, plan, outDir, pool)
	if err != nil {
		return fail(stderr, err)
	}
	m2 := manifest.Build(inputs, plan, res2)
	h2, err := manifest.Hash(m2)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	stable := h1 == h2

	out, code := finish("resume", inputs, plan, res2, outDir, workdir, stderr)
	if code != 0 {
		return code
	}
	out.ManifestStable = &stable
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
	if !stable {
		fmt.Fprintln(stderr, "manifest hash changed across resume")
		return 4
	}
	return 0
}
