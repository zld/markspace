package recovery_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"regress/internal/fault"
	"regress/internal/recovery"
)

func fakeHash(content string) recovery.HashProvider {
	return func(_ string) (string, error) {
		h := sha256.Sum256([]byte(content))
		return hex.EncodeToString(h[:]), nil
	}
}

func writeSegment(t *testing.T, root, job, name, content string) {
	t.Helper()
	dir := filepath.Join(root, job)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStateMachineTransitions(t *testing.T) {
	j, err := recovery.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Create("job", "cfg", 1); err != nil {
		t.Fatal(err)
	}
	// Re-creating with the same config hash is idempotent.
	if _, err := j.Create("job", "cfg", 2); err != nil {
		t.Fatal(err)
	}
	for _, st := range []recovery.State{
		recovery.StateProcessing, recovery.StateStaged,
		recovery.StatePublished, recovery.StateCommitted,
	} {
		if _, err := j.Transition("job", st, 3); err != nil {
			t.Fatalf("transition to %s: %v", st, err)
		}
	}
	if got := j.Get("job").State; got != recovery.StateCommitted {
		t.Fatalf("state = %s, want committed", got)
	}
	// Backward transitions are rejected.
	if _, err := j.Transition("job", recovery.StatePlanned, 4); err == nil {
		t.Fatal("backward transition was allowed")
	}
	// Unknown job key is rejected.
	if _, err := j.Transition("ghost", recovery.StateProcessing, 4); err == nil {
		t.Fatal("unknown job transition was allowed")
	}
}

func TestConfigHashConflictRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	j, _ := recovery.Open(dir)
	if _, err := j.Create("job", "cfg-A", 1); err != nil {
		t.Fatal(err)
	}
	_, err := j.Create("job", "cfg-B", 2)
	if err == nil || !fault.HasCode(err, fault.CodeContentConflict) {
		t.Fatalf("want content conflict, got %v", err)
	}
}

func TestResumeClaimsMatchingProducts(t *testing.T) {
	root := t.TempDir()
	j, _ := recovery.Open(filepath.Join(root, "journal"))
	rec, _ := j.Create("job-1", "cfg", 1)
	if _, err := j.Transition("job-1", recovery.StateProcessing, 2); err != nil {
		t.Fatal(err)
	}
	segs := []recovery.SegmentRecord{
		{File: "a-1.m4s", SHA256: mustHex("seg1")},
		{File: "a-2.m4s", SHA256: mustHex("seg2")},
	}
	if err := j.SetSegments("job-1", "manifest.json", segs, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Transition("job-1", recovery.StatePublished, 4); err != nil {
		t.Fatal(err)
	}
	writeSegment(t, filepath.Join(root, "pub"), "job-1", "a-1.m4s", "seg1")
	writeSegment(t, filepath.Join(root, "pub"), "job-1", "a-2.m4s", "seg2")

	// Hash function maps each file name to its known content digest.
	hp := func(path string) (string, error) {
		name := filepath.Base(path)
		content := map[string]string{"a-1.m4s": "seg1", "a-2.m4s": "seg2"}[name]
		return fakeHash(content)(path)
	}
	res, err := j.Resume("job-1", "cfg", filepath.Join(root, "pub"), 5, hp)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Claimed || res.Recovered != 1 || res.SegmentsOK != 2 ||
		res.Published != 0 || res.Duplicate != 0 {
		t.Fatalf("claim result wrong: %+v", res)
	}
	if j.Get("job-1").State != recovery.StateCommitted {
		t.Fatal("resume did not mark the job committed")
	}
	_ = rec
}

func TestResumeRejectsContentMismatch(t *testing.T) {
	root := t.TempDir()
	j, _ := recovery.Open(filepath.Join(root, "journal"))
	_, _ = j.Create("job-9", "cfg", 1)
	_ = j.SetSegments("job-9", "manifest.json", []recovery.SegmentRecord{
		{File: "a-1.m4s", SHA256: mustHex("original")},
	}, 2)
	_, _ = j.Transition("job-9", recovery.StatePublished, 3)
	writeSegment(t, filepath.Join(root, "pub"), "job-9", "a-1.m4s", "tampered")

	hp := func(path string) (string, error) { return fakeHash("tampered")(path) }
	res, err := j.Resume("job-9", "cfg", filepath.Join(root, "pub"), 4, hp)
	if err == nil || !fault.HasCode(err, fault.CodeContentConflict) {
		t.Fatalf("want content conflict, got %v", err)
	}
	if !res.Conflict || res.Claimed {
		t.Fatalf("conflict verdict wrong: %+v", res)
	}
	// The state must not have advanced after a refused overwrite.
	if j.Get("job-9").State != recovery.StatePublished {
		t.Fatal("conflict incorrectly advanced journal state")
	}
}

func TestJournalPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	j, _ := recovery.Open(dir)
	_, _ = j.Create("persist", "cfg", 1)
	_, _ = j.Transition("persist", recovery.StateStaged, 2)

	j2, err := recovery.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := j2.Get("persist")
	if rec == nil || rec.State != recovery.StateStaged || rec.ConfigHash != "cfg" {
		t.Fatalf("journal did not persist: %+v", rec)
	}
}

func TestNeedsRepublishSemantics(t *testing.T) {
	if !recovery.NeedsRepublish(nil) {
		t.Fatal("nil record should require publish")
	}
	for _, st := range []recovery.State{
		recovery.StatePlanned, recovery.StateProcessing, recovery.StateStaged,
	} {
		if !recovery.NeedsRepublish(&recovery.Record{State: st}) {
			t.Fatalf("state %s should require republish", st)
		}
	}
	for _, st := range []recovery.State{recovery.StatePublished, recovery.StateCommitted} {
		if recovery.NeedsRepublish(&recovery.Record{State: st}) {
			t.Fatalf("state %s should not require republish", st)
		}
	}
}

func mustHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
