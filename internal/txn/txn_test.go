package txn

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWALAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Begin(StageMetadata, "staging/metadata"); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(StageMetadata); err != nil {
		t.Fatal(err)
	}
	if err := w.Begin(StageRecovery, "staging/recovery"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen and replay: metadata committed, recovery begun only.
	w2, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	states, err := w2.States()
	if err != nil {
		t.Fatal(err)
	}
	if !states[StageMetadata].Committed {
		t.Fatal("metadata should be committed")
	}
	if !states[StageRecovery].Begun || states[StageRecovery].Committed {
		t.Fatalf("recovery should be begun-not-committed: %+v", states[StageRecovery])
	}
	done, err := w2.CommittedStages()
	if err != nil || len(done) != 1 || done[0] != StageMetadata {
		t.Fatalf("committed stages wrong: %v err=%v", done, err)
	}
}

func TestCommitProducesVerifiableManifest(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Begin(StageRender); err != nil {
		t.Fatal(err)
	}
	if _, err := m.WriteStaging(StageRender, "frame-000.bin", []byte("pixels")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.WriteStaging(StageRender, "sub/frame-001.bin", []byte("more pixels")); err != nil {
		t.Fatal(err)
	}
	man, err := m.Commit(StageRender)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(man.Artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(man.Artifacts))
	}
	if err := m.VerifyManifest(StageRender); err != nil {
		t.Fatalf("verify after commit: %v", err)
	}
	// Staging must be gone; committed directory holds the files.
	if _, err := os.Stat(filepath.Join(m.dir, "staging", StageRender)); !os.IsNotExist(err) {
		t.Fatal("staging directory must be removed after commit")
	}
	if _, err := os.Stat(filepath.Join(m.CommittedDir(StageRender), "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
}

func TestRollbackLeavesNoHalfArtifacts(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Begin(StageAlign); err != nil {
		t.Fatal(err)
	}
	if _, err := m.WriteStaging(StageAlign, "partial.json", []byte("{incomplete")); err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(StageAlign); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "staging", StageAlign)); !os.IsNotExist(err) {
		t.Fatal("rolled-back staging must not exist")
	}
	if _, err := os.Stat(m.CommittedDir(StageAlign)); !os.IsNotExist(err) {
		t.Fatal("rolled-back stage must have no committed directory")
	}
	states, _ := m.WAL().States()
	if !states[StageAlign].Aborted || states[StageAlign].Committed {
		t.Fatalf("abort not journaled correctly: %+v", states[StageAlign])
	}
}

func TestRecoverCleansUncommittedStage(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Commit metadata durably, then begin recovery and write files but crash
	// before committing (no Commit call; just close).
	if err := m.Begin(StageMetadata); err != nil {
		t.Fatal(err)
	}
	if _, err := m.WriteStaging(StageMetadata, "meta.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Commit(StageMetadata); err != nil {
		t.Fatal(err)
	}
	if err := m.Begin(StageRecovery); err != nil {
		t.Fatal(err)
	}
	if _, err := m.WriteStaging(StageRecovery, "stream.bin", []byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate restart.
	m2, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	committed, err := m2.Recover()
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(committed) != 1 || committed[0] != StageMetadata {
		t.Fatalf("recovered committed stages wrong: %v", committed)
	}
	if _, err := os.Stat(filepath.Join(dir, "staging", StageRecovery)); !os.IsNotExist(err) {
		t.Fatal("uncommitted staging must be cleaned on recovery")
	}
	if err := m2.VerifyManifest(StageMetadata); err != nil {
		t.Fatalf("committed stage must still verify: %v", err)
	}
	states, _ := m2.WAL().States()
	if !states[StageRecovery].Aborted {
		t.Fatal("recovery of a crashed stage must journal an abort")
	}
}

func TestRecoverDetectsTamperedArtifact(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Begin(StageRender); err != nil {
		t.Fatal(err)
	}
	if _, err := m.WriteStaging(StageRender, "f.bin", []byte("original")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Commit(StageRender); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	// Tamper with a committed artifact behind the manager's back.
	target := filepath.Join(dir, "committed", StageRender, "f.bin")
	if err := os.WriteFile(target, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	m2, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if _, err := m2.Recover(); err == nil {
		t.Fatal("tampered artifact must fail manifest verification")
	}
}

func TestRepeatedCommitHashesStable(t *testing.T) {
	build := func() string {
		d := t.TempDir()
		m, err := NewManager(d)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Begin(StageReport); err != nil {
			t.Fatal(err)
		}
		if _, err := m.WriteStaging(StageReport, "r.json", []byte(`{"a":1}`)); err != nil {
			t.Fatal(err)
		}
		man, err := m.Commit(StageReport)
		if err != nil {
			t.Fatal(err)
		}
		m.Close()
		return man.Artifacts[0].SHA256
	}
	if build() != build() {
		t.Fatal("identical content must produce identical hashes across runs")
	}
}
