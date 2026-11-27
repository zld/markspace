package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"regress/internal/fixture"
	"regress/internal/manifest"
	"regress/internal/model"
)

func TestCleanRunHasNoMissingOrDuplicates(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, fixture.FaultNone)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Measure.Counts.Missing != 0 || res.Measure.Counts.Duplicate != 0 {
		t.Fatalf("clean run counts: %+v", res.Measure.Counts)
	}
	// Every rendition boundary error must stay within the one-tick budget.
	for _, b := range res.Measure.Boundaries {
		if b.MaxAbsError > 1 {
			t.Errorf("%s boundary error %d > 1 tick", b.Rendition, b.MaxAbsError)
		}
	}
	// The planned constant delay surfaces in the quantiles.
	if res.Measure.Delay.Count == 0 || res.Measure.Delay.Min != 1500 {
		t.Fatalf("delay stats = %+v", res.Measure.Delay)
	}
	// Evidence must carry real frame, audio and alignment numbers.
	if len(res.Evidence.Frames) == 0 || len(res.Evidence.Audio) == 0 {
		t.Fatal("evidence missing frames or audio")
	}
	if res.Evidence.MaxAlignError != 1500 {
		t.Fatalf("alignment error = %d, want 1500", res.Evidence.MaxAlignError)
	}
	if res.Recovered || res.Republished != 0 {
		t.Fatalf("fresh run reported recovery: %+v", res)
	}
}

func TestTruncateFailsLocatableAndNonDestructive(t *testing.T) {
	dir := t.TempDir()
	_, err := Run(dir, fixture.FaultTruncate)
	if err == nil {
		t.Fatal("truncate run must fail")
	}
	if !errors.Is(err, model.ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
	me := model.AsError(err)
	if me == nil || me.Stage != model.StageParse || me.Offset <= 0 {
		t.Fatalf("error not locatable: %+v", me)
	}
	// Repeating the failed run must keep failing, not report a clean run.
	if _, err := Run(dir, fixture.FaultTruncate); err == nil {
		t.Fatal("second truncated run reported success")
	}
}

func TestWrapScenarioStaysOnGrid(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, fixture.FaultWrap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Measure.Counts.Missing != 0 || res.Measure.Counts.Duplicate != 0 {
		t.Fatalf("wrap altered event counts: %+v", res.Measure.Counts)
	}
	for _, b := range res.Measure.Boundaries {
		if b.MaxAbsError > 1 {
			t.Errorf("%s boundary error %d after wrap", b.Rendition, b.MaxAbsError)
		}
	}
}

func TestAfterArtifactRecoveryCommitsOnce(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(dir, fixture.FaultAfterArtifact); err == nil {
		t.Fatal("first attempt should stop before commit")
	}

	first, err := Run(dir, fixture.FaultAfterArtifact)
	if err != nil {
		t.Fatalf("recovery run: %v", err)
	}
	if !first.Recovered {
		t.Fatal("recovery run must be flagged recovered")
	}
	if first.Republished != 0 {
		t.Fatalf("recovery republished %d keys", first.Republished)
	}
	if first.ArtifactCount != 1 {
		t.Fatalf("artifacts = %d, want 1 (reused, not rewritten)",
			first.ArtifactCount)
	}
	manifestPath := filepath.Join(dir, string(fixture.FaultAfterArtifact),
		"manifest.txt")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	// A further run must be byte-identical and not report republication.
	second, err := Run(dir, fixture.FaultAfterArtifact)
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if second.Republished != 0 {
		t.Fatalf("third run republished %d keys", second.Republished)
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("manifest changed across an idempotent rerun")
	}
	if second.ManifestSHA != first.ManifestSHA {
		t.Fatalf("manifest hash drifted: %s vs %s",
			first.ManifestSHA, second.ManifestSHA)
	}
}

func TestManifestIsByteStableAcrossFreshRuns(t *testing.T) {
	a, err := Run(t.TempDir(), fixture.FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(t.TempDir(), fixture.FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	if a.ManifestSHA != b.ManifestSHA {
		t.Fatalf("identical inputs produced different manifests: %s vs %s",
			a.ManifestSHA, b.ManifestSHA)
	}
	// The reported hash must match the bytes actually on disk.
	raw, err := os.ReadFile(a.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Sha256(raw); got != a.ManifestSHA {
		t.Fatalf("reported hash %s != file hash %s", a.ManifestSHA, got)
	}
}

func TestScenariosAreIsolatedUnderOneWorkDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(dir, fixture.FaultNone); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, fixture.FaultWrap); err != nil {
		t.Fatal(err)
	}
	// The truncate scenario fails, but it must not disturb the others.
	if _, err := Run(dir, fixture.FaultTruncate); err == nil {
		t.Fatal("truncate should fail")
	}
	again, err := Run(dir, fixture.FaultNone)
	if err != nil {
		t.Fatalf("clean scenario disturbed by siblings: %v", err)
	}
	if again.Measure.Counts.Missing != 0 {
		t.Fatalf("counts after sibling runs: %+v", again.Measure.Counts)
	}
}

func TestEventsFaultCountsLosses(t *testing.T) {
	res, err := Run(t.TempDir(), fixture.FaultEvents)
	if err != nil {
		t.Fatal(err)
	}
	if res.Measure.Counts.Missing != 1 || res.Measure.Counts.Duplicate != 1 {
		t.Fatalf("events counts: %+v", res.Measure.Counts)
	}
	if res.Evidence.MissingEvents != 1 {
		t.Fatalf("evidence missing = %d", res.Evidence.MissingEvents)
	}
}

func TestPathGuardRefusesEscape(t *testing.T) {
	// A work dir whose scenario subtree would escape is not reachable here,
	// but confirm the artifact path guard rejects traversal through the
	// pipeline's job store regardless of the fault name.
	if err := validateFault(fixture.Fault("../../etc")); err == nil ||
		!strings.Contains(err.Error(), "unknown fault") {
		t.Fatalf("bad fault accepted: %v", err)
	}
}
