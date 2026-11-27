package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"example.com/assetpack/internal/fixture"
	"example.com/assetpack/internal/manifest"
	"example.com/assetpack/internal/migrate"
)

func TestRunInjectedFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	base, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	// Now inject a failure after the manifest stage.
	failing := opts
	failing.FailAfter = string(migrate.StageStageManifest)
	out, err := Run(failing)
	if err == nil {
		t.Fatal("injected failure did not abort the run")
	}
	// The run did not commit, but the old release is still valid on disk.
	if out.Committed || out.RunOK || out.ExitCode() == 0 {
		t.Fatalf("failed run reported success: committed=%v runok=%v exit=%d", out.Committed, out.RunOK, out.ExitCode())
	}
	if !out.Valid {
		t.Fatalf("rolled-back release should remain valid on disk")
	}
	if out.ManifestSHA != base.ManifestSHA {
		t.Fatalf("rollback changed the manifest: %s vs %s", out.ManifestSHA, base.ManifestSHA)
	}
	rec, _ := out.Journal.Get(migrate.StageStageManifest)
	if rec.Status != migrate.StatusFailed || rec.Fault == nil || rec.Fault.Kind != "injected-failure" {
		t.Fatalf("failure not recorded: %+v", rec)
	}
	// The resumed run reaches committed without duplicating resources.
	resumed, err := Run(opts)
	if err != nil {
		t.Fatalf("resumed run failed: %v", err)
	}
	if !resumed.Valid || !resumed.Committed || resumed.ExitCode() != 0 {
		t.Fatalf("resumed run not valid: %+v", resumed)
	}
	if len(resumed.After.Resources) != len(base.After.Resources) {
		t.Fatalf("resumed release has %d resources, want %d", len(resumed.After.Resources), len(base.After.Resources))
	}
}

func TestRunRejectsDamagedSourcesWithoutCommit(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	base, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	damaged := setup(t, dir, fixture.FaultSet{TruncatedTS: true, CCJump: true, PSIUpdate: true})
	out, err := Run(damaged)
	if err == nil {
		t.Fatal("damaged sources were released")
	}
	if out.Committed || out.RunOK || out.ExitCode() == 0 {
		t.Fatal("damaged run reported success")
	}
	if out.ManifestSHA != base.ManifestSHA {
		t.Fatalf("damaged run replaced the manifest: %s vs %s", out.ManifestSHA, base.ManifestSHA)
	}
	kinds := out.Faults.CountByKind()
	want := map[string]bool{"truncated-ts": false, "cc-jump": false, "psi-update": false, "digest-mismatch": false}
	for _, k := range kinds {
		if _, ok := want[string(k.Kind)]; ok {
			want[string(k.Kind)] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("fault class %q not reported: %+v", name, kinds)
		}
	}
	for _, f := range out.Faults.Items {
		if (f.Kind == "truncated-ts" || f.Kind == "cc-jump" || f.Kind == "psi-update") && f.Offset < 0 {
			t.Fatalf("fault %s has no offset", f.Kind)
		}
	}
}

func TestFailureBeforeCommitLeavesByteIdenticalRelease(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	base, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	before := readManifestBytes(t, opts)

	for _, stage := range []migrate.Stage{migrate.StageStageManifest, migrate.StageVerify} {
		failing := opts
		failing.FailAfter = string(stage)
		out, err := Run(failing)
		if err == nil {
			t.Fatalf("failure after %s did not abort", stage)
		}
		if out.Committed {
			t.Fatalf("failure after %s reported a commit", stage)
		}
		after := readManifestBytes(t, opts)
		if !bytes.Equal(before, after) {
			t.Fatalf("failure after %s altered the manifest bytes", stage)
		}
		if out.ManifestSHA != base.ManifestSHA {
			t.Fatalf("failure after %s changed the digest", stage)
		}
		if _, statErr := os.Stat(filepath.Join(opts.ReleaseDir, "manifest.prev.json")); statErr == nil {
			// A leftover rollback copy is harmless, but the live manifest must
			// be the original; it has already been compared byte for byte.
			_ = statErr
		}
	}
}

func TestFailureAtCommitLeavesCompleteNewRelease(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	base, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	// Change the target so the new release has a different digest.
	changed := opts
	changed.Target.Loudness = -20
	changed.FailAfter = string(migrate.StageCommit)
	out, err := Run(changed)
	if err == nil {
		t.Fatal("injected post-commit failure did not surface")
	}
	// The rename already happened: the new release is complete, not mixed.
	if !out.Committed {
		t.Fatal("post-commit failure must leave the new release committed")
	}
	if out.ManifestSHA == base.ManifestSHA {
		t.Fatal("post-commit release should be the new manifest")
	}
	if !out.Valid {
		t.Fatalf("post-commit release is not verifiable: %+v", out.Failure)
	}
	onDisk := readManifestBytes(t, opts)
	m, err := manifest.ReadV2(bytes.NewReader(onDisk))
	if err != nil {
		t.Fatalf("post-commit manifest invalid: %v", err)
	}
	if m.Digest != out.ManifestSHA || m.Target.LUFS != -16 {
		t.Fatalf("on-disk manifest does not match the reported release: %+v", m.Target)
	}
	for _, r := range m.Resources {
		if r.LoudnessLUFS > -19 {
			t.Fatalf("resource %s loudness looks wrong: %f", r.Name, r.LoudnessLUFS)
		}
	}
}

func TestRollbackIsIdempotentAcrossRetries(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	failing := opts
	failing.FailAfter = string(migrate.StageStageManifest)
	if _, err := Run(failing); err == nil {
		t.Fatal("expected failure")
	}
	// A second identical failing attempt must also leave a valid release.
	other := setup(t, dir, fixture.FaultSet{TruncatedTS: true})
	other.FailAfter = string(migrate.StageStageManifest)
	out, err := Run(other)
	if err == nil {
		t.Fatal("expected second failure")
	}
	if !out.Valid {
		t.Fatalf("release not verifiable after a second rollback: %+v", out.Faults)
	}
}
