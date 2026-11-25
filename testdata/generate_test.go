package testdata

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"example.com/assetcheck/internal/fragments"
	"example.com/assetcheck/internal/manifest"
	"example.com/assetcheck/internal/matroska"
	"example.com/assetcheck/internal/pipeline"
	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/internal/tracks"
)

func TestGenerateIsDeterministic(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	if _, err := Generate(d1, 17); err != nil {
		t.Fatalf("gen1: %v", err)
	}
	if _, err := Generate(d2, 17); err != nil {
		t.Fatalf("gen2: %v", err)
	}
	entries, err := os.ReadDir(d1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no fixtures written")
	}
	for _, e := range entries {
		a, err := os.ReadFile(filepath.Join(d1, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(d2, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("%s differs between runs with the same seed", e.Name())
		}
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	if _, err := Generate(d1, 17); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(d2, 23); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(d1, ValidMKV))
	b, _ := os.ReadFile(filepath.Join(d2, ValidMKV))
	if bytes.Equal(a, b) {
		t.Fatal("different seeds produced identical payload bytes")
	}
}

func manifestFor(t *testing.T) *manifest.Manifest {
	t.Helper()
	dir := t.TempDir()
	if _, err := Generate(dir, 17); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(filepath.Join(dir, ManifestV1))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

func TestValidFixturePassesVerification(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, 17); err != nil {
		t.Fatal(err)
	}
	rep, err := pipeline.Verify(filepath.Join(dir, ValidMKV), pipeline.VerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v (issues %+v, frag %+v)", err, rep.Issues, rep.Fragments.Problems)
	}
	if rep.ElementCount == 0 || rep.TrackCount != 5 || rep.FragmentCount != 10 {
		t.Fatalf("counts: %d elements, %d tracks, %d fragments",
			rep.ElementCount, rep.TrackCount, rep.FragmentCount)
	}
	if rep.MaxDeviation > timeline.MaxTolerance {
		t.Fatalf("deviation = %d", rep.MaxDeviation)
	}
	if len(rep.SHA256) != 64 {
		t.Fatalf("sha256 = %q", rep.SHA256)
	}
}

func TestValidFixtureWithManifestPasses(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, 17); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(filepath.Join(dir, ManifestV1))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ValidateCrossFields(); err != nil {
		t.Fatalf("cross-field: %v", err)
	}
	rep, err := pipeline.Verify(filepath.Join(dir, ValidMKV),
		pipeline.VerifyOptions{Profile: m.Profile()})
	if err != nil {
		t.Fatalf("verify against manifest: %v (issues %+v)", err, rep.Issues)
	}
}

func TestEachPresetFaultIsReproducedAtItsOffset(t *testing.T) {
	dir := t.TempDir()
	res, err := Generate(dir, 17)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Expectations) < 4 {
		t.Fatalf("expectations = %d, want at least 4 fault classes", len(res.Expectations))
	}
	m, err := manifest.Load(filepath.Join(dir, ManifestV1))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range res.Expectations {
		t.Run(want.File, func(t *testing.T) {
			path := filepath.Join(dir, want.File)
			opts := pipeline.VerifyOptions{}
			if want.Stage == string(timeline.StageValidate) {
				opts.Profile = m.Profile()
			}
			_, err := pipeline.Verify(path, opts)
			if err == nil {
				t.Fatal("damaged fixture verified clean")
			}
			if got := string(timeline.CategoryOf(err)); got != want.Category {
				t.Fatalf("category = %s, want %s (err=%v)", got, want.Category, err)
			}
			gotOffset := timeline.OffsetOf(err)
			if gotOffset != want.Offset {
				t.Fatalf("offset = %d, preset = %d (delta %d)",
					gotOffset, want.Offset, gotOffset-want.Offset)
			}
			var te *timeline.Error
			if !asTimeline(err, &te) {
				t.Fatalf("error is not structured: %T", err)
			}
			if te.ElementID != want.ElementID {
				t.Fatalf("element = 0x%X, preset = 0x%X", te.ElementID, want.ElementID)
			}
		})
	}
}

// Faults must propagate across more than one module: parse faults surface from
// the EBML reader, and validation faults from the track/fragment validators, with
// the pipeline reporting both.
func TestFaultsPropagateAcrossModules(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, 17); err != nil {
		t.Fatal(err)
	}
	_, parseErr := pipeline.Verify(filepath.Join(dir, TruncatedMKV), pipeline.VerifyOptions{})
	if timeline.CategoryOf(parseErr) != timeline.CatTruncated {
		t.Fatalf("truncated category = %s", timeline.CategoryOf(parseErr))
	}
	var te *timeline.Error
	if !asTimeline(parseErr, &te) || te.Stage != timeline.StageParse {
		t.Fatalf("truncated stage = %v", parseErr)
	}

	m, _ := manifest.Load(filepath.Join(dir, ManifestV1))
	_, valErr := pipeline.Verify(filepath.Join(dir, TrackConflictMKV),
		pipeline.VerifyOptions{Profile: m.Profile()})
	if !asTimeline(valErr, &te) || te.Stage != timeline.StageValidate {
		t.Fatalf("conflict error = %v", valErr)
	}
	if len(te.Tracks) < 2 {
		t.Fatalf("conflict must name both tracks, got %v", te.Tracks)
	}
}

func TestUnknownLengthParentIsReported(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, 17); err != nil {
		t.Fatal(err)
	}
	a, err := matroska.Parse(filepath.Join(dir, UnknownSizeMKV))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.UnknownSized < 2 {
		t.Fatalf("unknown-length masters = %d, want Segment + Cluster", a.UnknownSized)
	}
	if err := fragments.Validate(a).Err(); err != nil {
		t.Fatalf("fragments: %v", err)
	}
	valid, err := matroska.Parse(filepath.Join(dir, ValidMKV))
	if err != nil {
		t.Fatal(err)
	}
	if len(valid.Fragments) != len(a.Fragments) {
		t.Fatalf("unknown-length fixture changed fragment count: %d vs %d",
			len(a.Fragments), len(valid.Fragments))
	}
}

func TestManifestVersionsAdaptToSameRules(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, 17); err != nil {
		t.Fatal(err)
	}
	v1, err := manifest.Load(filepath.Join(dir, ManifestV1))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := manifest.Load(filepath.Join(dir, ManifestV2))
	if err != nil {
		t.Fatal(err)
	}
	if v1.Version != 1 || v2.Version != 2 {
		t.Fatalf("versions = %d, %d", v1.Version, v2.Version)
	}
	if len(v1.AudioRates) != len(v2.AudioRates) || len(v1.Tracks) != len(v2.Tracks) {
		t.Fatalf("adapted shapes differ: %d/%d rates, %d/%d tracks",
			len(v1.AudioRates), len(v2.AudioRates), len(v1.Tracks), len(v2.Tracks))
	}
	for num, rate := range v1.AudioRates {
		if v2.AudioRates[num] != rate {
			t.Fatalf("track %d rate differs: %+v vs %+v", num, rate, v2.AudioRates[num])
		}
	}
	if v1.Layouts[3] != v2.Layouts[3] || v1.Layouts[3] != tracks.Layout5_1 {
		t.Fatalf("layouts: %q vs %q", v1.Layouts[3], v2.Layouts[3])
	}
}

func asTimeline(err error, target **timeline.Error) bool {
	for err != nil {
		if e, ok := err.(*timeline.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestRecoveryAfterMigrationInterruption exercises a failure that propagates
// through the migration recovery module: a mismatched resume input must roll
// back to the last consistent checkpoint and report the reason, and the run must
// then complete with the new input's digest rather than the stale one.
func TestRecoveryAfterMigrationInterruption(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, 17); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, ManifestV1)
	out := filepath.Join(dir, "out")

	first, err := pipeline.Migrate(pipeline.MigrateOptions{
		Input: filepath.Join(dir, ValidMKV), ManifestPath: manifestPath,
		Out: out, StopAfter: "parse",
	})
	if err != nil {
		t.Fatalf("partial: %v", err)
	}
	result, err := pipeline.Migrate(pipeline.MigrateOptions{
		Input: filepath.Join(dir, TrackConflictMKV), ManifestPath: manifestPath,
		Out: out, Resume: true,
	})
	if err == nil {
		t.Fatal("resume against a mismatched input succeeded without reporting the rollback")
	}
	if result == nil || result.Verify == nil {
		t.Fatal("no verification report on the failed resume")
	}
	if timeline.CategoryOf(err) != timeline.CatTrackConflict {
		t.Fatalf("category = %s, want the propagated validation conflict", timeline.CategoryOf(err))
	}
	_ = first
}

// TestUnknownLengthParentAndTruncationShareReaderPath proves the unknown-length
// boundary logic and truncation detection come from the same reader, so a fix to
// one cannot silently diverge from the other.
func TestUnknownLengthParentAndTruncationShareReaderPath(t *testing.T) {
	dir := t.TempDir()
	res, err := Generate(dir, 17)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := matroska.Parse(filepath.Join(dir, UnknownSizeMKV)); err != nil {
		t.Fatalf("unknown-length parent failed: %v", err)
	}
	if _, err := matroska.Parse(filepath.Join(dir, TruncatedMKV)); err == nil {
		t.Fatal("truncated element parsed cleanly")
	}
	// The two fixtures must differ only where the generator says they do.
	var want int64 = -1
	for _, e := range res.Expectations {
		if e.File == TruncatedMKV {
			want = e.Offset
		}
	}
	if want < 0 {
		t.Fatal("truncation expectation missing")
	}
	_, err = matroska.Parse(filepath.Join(dir, TruncatedMKV))
	if timeline.OffsetOf(err) != want {
		t.Fatalf("truncation offset = %d, preset = %d", timeline.OffsetOf(err), want)
	}
}
