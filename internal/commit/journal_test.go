package commit_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/commit"
	"mediapack/internal/manifest"
)

func newManifestWithArtifact(t *testing.T, dir string) *manifest.Manifest {
	t.Helper()
	m := manifest.New("source.ts", 1, []string{"r0"})
	m.Artifacts = append(m.Artifacts, manifest.Artifact{
		ID: "r0/seg_0000", IntervalIndex: 0, RateIndex: 0, RateName: "r0",
		RelPath: "segments/r0/seg_0000.frag", Status: manifest.StatusPending,
	})
	if err := m.Save(dir); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestPrepareCrashThenReplayAndCommit proves the write->prepared->crash(20)
// ->replay->adopt protocol used by the acceptance flow.
func TestPrepareCrashThenReplayAndCommit(t *testing.T) {
	dir := t.TempDir()
	m := newManifestWithArtifact(t, dir)
	j, err := commit.OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("TSFRAG"), 40)
	if err := commit.Prepare(dir, m, j, &m.Artifacts[0], bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if m.Artifacts[0].Status != manifest.StatusPrepared || m.Artifacts[0].SHA256 == "" {
		t.Fatal("artifact must be prepared with hash")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulated process restart: replay finds exactly one uncommitted prepare.
	entries, err := commit.Replay(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending := commit.PreparedButUncommitted(entries)
	if len(pending) != 1 || pending[0].ID != "r0/seg_0000" {
		t.Fatalf("pending=%+v want one", pending)
	}

	// Recovery loads the ledger fresh and commits the hash-valid temp product.
	m2, err := manifest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	j2, err := commit.OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	a, adopted, err := commit.Commit(dir, m2, j2, "r0/seg_0000")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !adopted || a.Status != manifest.StatusCommitted {
		t.Fatalf("adopted=%v status=%s", adopted, a.Status)
	}
	final, _ := manifest.Resolve(dir, "segments/r0/seg_0000.frag")
	got, err := os.ReadFile(final)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatal("final fragment content mismatch")
	}
	if _, err := os.Stat(final + ".part"); !os.IsNotExist(err) {
		t.Fatal("temp file should be renamed away")
	}

	// A second commit must be a no-op (no duplicate publication).
	_, adopted2, err := commit.Commit(dir, m2, j2, "r0/seg_0000")
	if err != nil || adopted2 {
		t.Fatalf("repeat commit adopted=%v err=%v, want no-op", adopted2, err)
	}
}

// TestCommitAdoptsRenamedFinal covers a crash after rename but before status.
func TestCommitAdoptsRenamedFinal(t *testing.T) {
	dir := t.TempDir()
	m := newManifestWithArtifact(t, dir)
	j, _ := commit.OpenJournal(dir)
	content := []byte("rename-crash-case")
	if err := commit.Prepare(dir, m, j, &m.Artifacts[0], bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	tmp, _ := manifest.Resolve(dir, m.Artifacts[0].TempRelPath)
	final, _ := manifest.Resolve(dir, m.Artifacts[0].RelPath)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, final); err != nil { // rename happened, status not saved
		t.Fatal(err)
	}
	m2, _ := manifest.Load(dir) // ledger still says prepared
	j2, _ := commit.OpenJournal(dir)
	defer j2.Close()
	a, adopted, err := commit.Commit(dir, m2, j2, "r0/seg_0000")
	if err != nil {
		t.Fatal(err)
	}
	if !adopted || a.Status != manifest.StatusCommitted {
		t.Fatalf("must adopt existing final: adopted=%v status=%s", adopted, a.Status)
	}
}
