package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediarun/internal/fault"
	"mediarun/internal/fixture"
	"mediarun/internal/verify"
)

// genFixture creates the standard fixture tree in a temp directory.
func genFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fixture")
	if err := fixture.Generate(dir); err != nil {
		t.Fatalf("generate fixture: %v", err)
	}
	return dir
}

// TestRunDeterminismResumeAndVerify is the core acceptance loop: first run
// builds everything, second run reuses all four stages with an identical
// output digest, and the standalone verifier agrees.
func TestRunDeterminismResumeAndVerify(t *testing.T) {
	in := genFixture(t)
	out := filepath.Join(t.TempDir(), "out")
	opts := Options{InputDir: in, OutDir: out, TargetBitrate: 1_200_000, Resume: true}

	r1, err := Run(opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if r1.ReusedStages != 0 {
		t.Fatalf("first run reused %d stages, want 0", r1.ReusedStages)
	}
	if bad := r1.Metrics.Check(verify.DefaultThresholds()); len(bad) != 0 {
		t.Fatalf("thresholds: %v", bad)
	}
	if r1.Metrics.RandomAccessHits != r1.Metrics.RandomAccessQueries {
		t.Fatalf("hit rate not 100%%: %+v", r1.Metrics)
	}

	r2, err := Run(opts)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if r2.ReusedStages != 4 {
		t.Fatalf("second run reused %d stages, want 4", r2.ReusedStages)
	}
	if r2.OutputDigest != r1.OutputDigest {
		t.Fatalf("output digest changed on identical input:\n%s\n%s",
			r1.OutputDigest, r2.OutputDigest)
	}

	vr, err := VerifyCommitted(in, out)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if vr.OutputDigest != r1.OutputDigest {
		t.Fatalf("verify digest %s != run digest %s", vr.OutputDigest, r1.OutputDigest)
	}
}

// TestChangedInputInvalidatesStages flips one payload byte: the input key
// changes, no stage may be reused, and the output digest must change.
func TestChangedInputInvalidatesStages(t *testing.T) {
	in := genFixture(t)
	out := filepath.Join(t.TempDir(), "out")
	opts := Options{InputDir: in, OutDir: out, TargetBitrate: 1_200_000, Resume: true}
	r1, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}

	seg := filepath.Join(in, "seg-v2-3.m4s")
	data, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-64] ^= 0xff // inside mdat payload: structure unchanged
	if err := os.WriteFile(seg, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Old committed provenance no longer verifies.
	if _, err := VerifyCommitted(in, out); err == nil {
		t.Fatal("verify succeeded after input mutation, want provenance failure")
	}

	r2, err := Run(opts)
	if err != nil {
		t.Fatalf("rerun after payload mutation: %v", err)
	}
	if r2.ReusedStages != 0 {
		t.Fatalf("reused %d stages after input byte changed, want 0", r2.ReusedStages)
	}
	if r2.OutputDigest == r1.OutputDigest {
		t.Fatal("output digest unchanged after source byte changed")
	}
}

// TestBoundaryFixturesClassified drives the three corrupt fixtures through
// the pipeline and checks stage, raw offset and exit-code mapping.
func TestBoundaryFixturesClassified(t *testing.T) {
	in := genFixture(t)
	cases := []struct {
		name  string
		stage fault.Stage
	}{
		{"truncated", fault.StageBoxHeader},
		{"zerots", fault.StageManifest},
		{"dupts", fault.StageRepair},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cin := filepath.Join(in, "corrupt", tc.name)
			cout := filepath.Join(t.TempDir(), "out-"+tc.name)
			_, err := Run(Options{InputDir: cin, OutDir: cout, TargetBitrate: 1_200_000})
			if err == nil {
				t.Fatalf("%s: expected failure", tc.name)
			}
			var fe *fault.Error
			if !errors.As(err, &fe) {
				t.Fatalf("%s: error is not *fault.Error: %T", tc.name, err)
			}
			if fe.Stage != tc.stage {
				t.Fatalf("%s: stage=%s want %s", tc.name, fe.Stage, tc.stage)
			}
			if fe.Offset < 0 {
				t.Fatalf("%s: missing raw offset: %v", tc.name, fe)
			}
			if code := fault.ExitCode(err); code != fault.ExitValidation {
				t.Fatalf("%s: exit code=%d want 2", tc.name, code)
			}
		})
	}
}

// TestCommitFailureExit3 ensures an unwritable output location maps to the
// commit/resume exit code.
func TestCommitFailureExit3(t *testing.T) {
	in := genFixture(t)
	fileAsOut := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(fileAsOut, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(Options{InputDir: in, OutDir: fileAsOut})
	if err == nil {
		t.Fatal("expected output creation failure")
	}
	if code := fault.ExitCode(err); code != fault.ExitCommitPhase {
		t.Fatalf("exit code=%d want 3 (%v)", code, err)
	}
}

// TestOutputInsideInputRejected covers the path boundary rule.
func TestOutputInsideInputRejected(t *testing.T) {
	in := genFixture(t)
	_, err := Run(Options{InputDir: in, OutDir: filepath.Join(in, "nested-out")})
	if err == nil || fault.ExitCode(err) != fault.ExitValidation {
		t.Fatalf("err=%v, want validation rejection", err)
	}
}

// TestQueryCommittedIndex checks point queries and both boundary errors.
func TestQueryCommittedIndex(t *testing.T) {
	in := genFixture(t)
	out := filepath.Join(t.TempDir(), "out")
	if _, err := Run(Options{InputDir: in, OutDir: out, TargetBitrate: 1_200_000}); err != nil {
		t.Fatal(err)
	}
	ix, err := LoadIndex(out)
	if err != nil {
		t.Fatal(err)
	}
	mid := ix.Entries[0].Tick + (ix.End-ix.Entries[0].Tick)/2
	e, err := QueryIndex(out, mid)
	if err != nil {
		t.Fatalf("mid query: %v", err)
	}
	if e.Tick > mid || e.Fragment == "" || e.SourceDigest == "" {
		t.Fatalf("bad entry: %+v", e)
	}
	if _, err := QueryIndex(out, ix.Entries[0].Tick-1); err == nil {
		t.Fatal("expected before-start boundary error")
	}
	if _, err := QueryIndex(out, ix.End); err == nil {
		t.Fatal("expected after-end boundary error")
	}
}

// TestHLSDiscontinuityMonotonic verifies the cross-discontinuity PTS jump
// stays monotonic on the unified timeline while alignment stays in range.
func TestHLSDiscontinuityMonotonic(t *testing.T) {
	in := genFixture(t)
	tl, err := buildTimeline(in, []string{"playlist.m3u8"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.HLS) != 1 {
		t.Fatalf("hls timelines=%d", len(tl.HLS))
	}
	tr, err := processHLS(in, tl.HLS[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Segments) != 4 {
		t.Fatalf("segments=%d want 4", len(tr.Segments))
	}
	sawDisc := false
	sawOffset := false
	for i, s := range tr.Segments {
		if i > 0 && s.ActualStart < tr.Segments[i-1].ActualStart {
			t.Fatalf("timeline went backwards at segment %d: %d < %d",
				i, s.ActualStart, tr.Segments[i-1].ActualStart)
		}
		if s.AlignmentErrorMs > 20 {
			t.Fatalf("segment %d alignment %dms > 20", i, s.AlignmentErrorMs)
		}
		if s.DiscontinuitySeq > 0 {
			sawDisc = true
		}
		if s.DiscontinuityOff != 0 {
			sawOffset = true
		}
	}
	if !sawDisc {
		t.Fatal("no post-discontinuity segment mapped")
	}
	if !sawOffset {
		t.Fatal("expected a nonzero discontinuity mapping offset")
	}
}

// TestSafeJoinRejectsTraversal checks manifest references cannot escape.
func TestSafeJoinRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	if _, err := safeJoin(base, "segments/a.m4s"); err != nil {
		t.Fatalf("relative nested path rejected: %v", err)
	}
	for _, bad := range []string{"../escape.m4s", "/etc/passwd", ""} {
		if _, err := safeJoin(base, bad); err == nil {
			t.Fatalf("safeJoin(%q) unexpectedly succeeded", bad)
		}
	}
}
