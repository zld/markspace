package pipeline

import (
	"os"
	"testing"

	"liveregress/internal/fixture"
)

// runCase generates one fixture case in a temp dir and runs the pipeline.
func runCase(t *testing.T, caseName string, opts Options) *Report {
	t.Helper()
	dir := t.TempDir()
	tree, err := fixture.GenerateOne(dir, caseName)
	if err != nil {
		t.Fatalf("generate %s: %v", caseName, err)
	}
	opts.Root = tree.Root
	opts.Case = caseName
	rep, err := Run(opts)
	if err != nil {
		t.Fatalf("run %s: %v", caseName, err)
	}
	return rep
}

func TestCleanCaseContract(t *testing.T) {
	rep := runCase(t, fixture.CaseClean, Options{})
	// Two variants plus two audio renditions each round-trip their declared
	// timeline exactly.
	if rep.ParseRoundtrip != rep.ParseTotal || rep.ParseTotal != 4 {
		t.Fatalf("parse roundtrip %d/%d", rep.ParseRoundtrip, rep.ParseTotal)
	}
	if rep.KeyframeHits != 6 || rep.KeyframeTotal != 6 {
		t.Fatalf("keyframe_hit=%d/%d", rep.KeyframeHits, rep.KeyframeTotal)
	}
	if rep.SegmentSHA256Match != 6 || rep.SegmentTotal != 6 {
		t.Fatalf("segment_sha256_match=%d/%d", rep.SegmentSHA256Match, rep.SegmentTotal)
	}
	if len(rep.Gaps) != 0 {
		t.Fatalf("continuity gaps: %+v", rep.Gaps)
	}
	if rep.ExitCode() != 0 {
		t.Fatalf("exit=%d faults=%v", rep.ExitCode(), rep.FaultLines())
	}
	// Every audio layer proves byte identity against its WAV source.
	wavLayers := 0
	for _, l := range rep.Layers {
		if l.WAV != nil {
			wavLayers++
			if l.WAV.MatchedBytes == 0 || l.WAV.Channels != 3 {
				t.Fatalf("wav check incomplete: %+v", l.WAV)
			}
		}
		if l.TS != nil && (l.TS.Samples == 0 || l.TS.RTPPackets == 0 || l.TS.RTPSeqGaps != 0) {
			t.Fatalf("ts probe incomplete: %+v", l.TS)
		}
	}
	if wavLayers != 2 {
		t.Fatalf("verified wav layers = %d", wavLayers)
	}
}

func TestCleanRerunIsIdentical(t *testing.T) {
	dir := t.TempDir()
	tree, err := fixture.GenerateOne(dir, fixture.CaseClean)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Root: tree.Root, Case: fixture.CaseClean}
	first, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	a, b := first.SegmentDigests(), second.SegmentDigests()
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("digest sets differ: %d vs %d", len(a), len(b))
	}
	for path, digest := range a {
		if b[path] != digest {
			t.Fatalf("%s changed between runs: %s -> %s", path, digest, b[path])
		}
	}
	// A resumed run must also match the cold run byte-for-byte.
	third, err := Run(Options{Root: tree.Root, Case: fixture.CaseClean, Recover: true})
	if err != nil {
		t.Fatal(err)
	}
	for path, digest := range a {
		if third.SegmentDigests()[path] != digest {
			t.Fatalf("resumed run changed %s", path)
		}
	}
}

func TestTruncatedCaseKeepsCompletePrefix(t *testing.T) {
	rep := runCase(t, fixture.CaseTruncated, Options{Recover: true})
	if rep.ExitCode() != 2 {
		t.Fatalf("exit=%d faults=%v", rep.ExitCode(), rep.FaultLines())
	}
	var truncFaults int
	for _, f := range rep.Faults {
		if f.Kind == "truncate" {
			truncFaults++
			if f.Offset <= 0 {
				t.Fatalf("truncation must report a raw offset: %v", f)
			}
			if !f.Recoverable {
				t.Fatalf("tail truncation must be recoverable: %v", f)
			}
		}
	}
	if truncFaults == 0 {
		t.Fatal("expected truncation faults")
	}
	// The truncated segments are partial, and nothing after them is committed.
	for _, l := range rep.Layers {
		for _, s := range l.Segments {
			if !s.Complete && s.Bytes == 0 {
				t.Fatalf("%s marked partial without a complete prefix", s.Path)
			}
		}
	}
}

func TestCorruptCaseIsolatesOnlyAffectedLayer(t *testing.T) {
	rep := runCase(t, fixture.CaseCorrupt, Options{Recover: true})
	if rep.ExitCode() != 2 {
		t.Fatalf("exit=%d faults=%v", rep.ExitCode(), rep.FaultLines())
	}
	isolated := map[string]bool{}
	healthy := 0
	for _, l := range rep.Layers {
		if l.Isolated {
			isolated[l.Layer] = true
		} else {
			healthy++
		}
	}
	if !isolated["a2"] {
		t.Fatalf("a2 should be isolated, layers=%+v", isolated)
	}
	if isolated["a1"] || isolated["v0"] || isolated["v1"] {
		t.Fatalf("only a2 should be isolated, got %v", isolated)
	}
	if healthy != 3 {
		t.Fatalf("healthy layers = %d", healthy)
	}
}

func TestNoIndexNeedsRebuild(t *testing.T) {
	typ := runCase(t, fixture.CaseNoIndex, Options{})
	if typ.KeyframeTotal != 0 {
		t.Fatalf("without rebuild, index check must not pass: %+v", typ.Layers)
	}
	rebuilt := runCase(t, fixture.CaseNoIndex, Options{RebuildIndex: true})
	if rebuilt.KeyframeHits != 6 || rebuilt.KeyframeTotal != 6 {
		t.Fatalf("rebuilt keyframe_hit=%d/%d", rebuilt.KeyframeHits, rebuilt.KeyframeTotal)
	}
	if rebuilt.ExitCode() != 0 {
		t.Fatalf("rebuilt exit=%d faults=%v", rebuilt.ExitCode(), rebuilt.FaultLines())
	}
	for _, l := range rebuilt.Layers {
		if l.Kind == "video" && !l.Rebuilt {
			t.Fatalf("%s did not report a rebuild", l.Layer)
		}
	}
}

func TestClockGapReportsDeviationAndLayer(t *testing.T) {
	rep := runCase(t, fixture.CaseClockGap, Options{Recover: true})
	if len(rep.Gaps) == 0 {
		t.Fatal("expected a clock gap")
	}
	for _, g := range rep.Gaps {
		if g.Layer != "v1" {
			t.Fatalf("gap attributed to %q, want v1", g.Layer)
		}
		if g.DeviationTicks != -45000 || g.Timescale != 90000 {
			t.Fatalf("gap deviation = %d/%d", g.DeviationTicks, g.Timescale)
		}
	}
	if rep.ExitCode() != 2 {
		t.Fatalf("exit=%d", rep.ExitCode())
	}
}

func TestInvalidMappingExitsOne(t *testing.T) {
	rep := runCase(t, fixture.CaseBadMapping, Options{})
	if rep.ExitCode() != 1 {
		t.Fatalf("exit=%d faults=%v", rep.ExitCode(), rep.FaultLines())
	}
}

func TestPathEscapeExitsOne(t *testing.T) {
	rep := runCase(t, fixture.CaseBadPath, Options{})
	if rep.ExitCode() != 1 {
		t.Fatalf("exit=%d faults=%v", rep.ExitCode(), rep.FaultLines())
	}
	found := false
	for _, f := range rep.Faults {
		if f.Kind == "path" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a path fault")
	}
}

func TestMissingRootIsRefused(t *testing.T) {
	if _, err := Run(Options{Root: "/definitely/not/here", Case: "x"}); err == nil {
		t.Fatal("expected an error for a missing root")
	}
}

// TestChangedInputRescansOnlyAffectedSegment commits a clean run, mutates one
// segment's bytes, and resumes. The untouched segments must reproduce their
// previous output digests while the changed one is rescanned.
func TestChangedInputRescansOnlyAffectedSegment(t *testing.T) {
	dir := t.TempDir()
	tree, err := fixture.GenerateOne(dir, fixture.CaseClean)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Root: tree.Root, Case: fixture.CaseClean}
	first, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	before := first.SegmentDigests()

	// Change one audio segment's source bytes without touching the others.
	target := tree.Root + "/a1/seg2.m4s"
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a payload byte late in the file (inside mdat).
	data[len(data)-1] ^= 0xFF
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}

	second, err := Run(Options{Root: tree.Root, Case: fixture.CaseClean, Recover: true})
	if err != nil {
		t.Fatal(err)
	}
	after := second.SegmentDigests()
	for path, digest := range before {
		if path == "a1/seg2.m4s" {
			continue
		}
		if after[path] != digest {
			t.Fatalf("untouched %s changed: %s -> %s", path, digest, after[path])
		}
	}
	if after["a1/seg2.m4s"] == before["a1/seg2.m4s"] {
		t.Fatal("changed segment produced the same output digest")
	}
	found := false
	for _, l := range second.Layers {
		for _, s := range l.Segments {
			if s.Path == "a1/seg2.m4s" && s.Rescanned {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("changed segment was not marked as rescanned")
	}
}
