package txn

import (
	"errors"
	"testing"

	"mediacheck/internal/fixture"
	"mediacheck/internal/provenance"
)

func TestPipelineRunAndReuse(t *testing.T) {
	dir := t.TempDir()
	if err := fixture.Generate(dir, fixture.CaseNormal); err != nil {
		t.Fatal(err)
	}
	oc, err := RunPipeline(dir, "both", "")
	if err != nil {
		t.Fatal(err)
	}
	if oc.IdentityState != "rebuilt" || oc.Result.Events != 6 || len(oc.Result.Index.Entries) != 6 {
		t.Fatalf("outcome = %+v events=%d", oc, oc.Result.Events)
	}
	// second run with identical identity reuses the committed result
	oc2, err := RunPipeline(dir, "both", "")
	if err != nil {
		t.Fatal(err)
	}
	if oc2.IdentityState != "reused" {
		t.Fatalf("second run state = %s, want reused", oc2.IdentityState)
	}
	if len(oc2.Result.Index.Entries) != 6 {
		t.Fatalf("reused run duplicated entries: %d", len(oc2.Result.Index.Entries))
	}
	// changing an input invalidates the committed result
	if err := fixture.Generate(dir, fixture.CaseTruncated); err != nil {
		t.Fatal(err)
	}
	oc3, err := RunPipeline(dir, "both", "")
	if err != nil {
		t.Fatal(err)
	}
	if oc3.IdentityState != "rebuilt" || oc3.Invalidation == "none" {
		t.Fatalf("outcome = %+v invalidation=%s", oc3, oc3.Invalidation)
	}
	if len(oc3.Result.Index.Anomalies) != 1 {
		t.Fatalf("anomalies = %+v", oc3.Result.Index.Anomalies)
	}
}

func TestPipelineCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	if err := fixture.Generate(dir, fixture.CaseNormal); err != nil {
		t.Fatal(err)
	}
	if _, err := RunPipeline(dir, "both", StageBuild); !errors.Is(err, ErrSimulatedCrash) {
		t.Fatalf("crash run err = %v", err)
	}
	oc, err := RunPipeline(dir, "both", "")
	if err != nil {
		t.Fatal(err)
	}
	rec := oc.Recovery
	if !rec.Interrupted || rec.ResumeStage != StageBuild || rec.RolledBack != 1 {
		t.Fatalf("recovery = %+v, want interrupted resume at build with 1 rollback", rec)
	}
	clean, err := CleanDigest(dir, "both")
	if err != nil {
		t.Fatal(err)
	}
	if got := provenance.DigestIndex(oc.Result.Index); got != clean {
		t.Fatalf("recovered digest %s != clean digest %s", got, clean)
	}
	// recovery is idempotent: rerunning reuses, no duplicate entries
	oc2, err := RunPipeline(dir, "both", "")
	if err != nil {
		t.Fatal(err)
	}
	if oc2.IdentityState != "reused" || len(oc2.Result.Index.Entries) != 6 {
		t.Fatalf("post-recovery run = %s entries=%d", oc2.IdentityState, len(oc2.Result.Index.Entries))
	}
}
