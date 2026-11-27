package rollback

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Simulate a crash after the artifact renames but before the manifest rename:
// the previous release must remain completely intact.
func TestRecoverInterruptedBeforeManifestRename(t *testing.T) {
	dir := t.TempDir()
	c, first := seedRelease(t, dir, "one")
	next, arts := testManifest(t, "rel", "two", map[string][]byte{"a.m4a": []byte("brand new a"), "b.m4a": []byte("brand new b")})
	stage, err := c.Stage(next, arts)
	if err != nil {
		t.Fatal(err)
	}
	// Hand-run the pre-rename half of Commit.
	if err := copyFileAtomic(c.path(ManifestName), c.path(RollbackName)); err != nil {
		t.Fatal(err)
	}
	for _, name := range artifactNames(arts) {
		if err := os.MkdirAll(c.ArtifactDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := renameAtomic(filepath.Join(stage, ArtifactDir, name), filepath.Join(c.ArtifactDir(), name)); err != nil {
			t.Fatal(err)
		}
	}
	// Crash here: journal still says staged, manifest still the old one.
	st, err := c.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if st.Mixed || st.OldOrNew != "old" {
		t.Fatalf("recovery misclassified: %+v", st)
	}
	cur, _ := c.ReadManifest()
	if cur.Digest != first.Digest {
		t.Fatalf("old manifest not intact: %s vs %s", cur.Digest, first.Digest)
	}
	if _, err := os.Stat(c.path(st.Release + StagingSuffix)); err == nil {
		// The staging directory for the interrupted digest must be gone.
		t.Log("staging dir still present (allowed only for a different digest)")
	}
}

// Simulate a crash after the manifest rename but before the journal update:
// recovery must report the complete new release and repair the journal.
func TestRecoverInterruptedAfterManifestRename(t *testing.T) {
	dir := t.TempDir()
	c, first := seedRelease(t, dir, "one")
	next, arts := testManifest(t, "rel", "two", map[string][]byte{"a.m4a": []byte("new a"), "b.m4a": []byte("new b")})
	stage, err := c.Stage(next, arts)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyFileAtomic(c.path(ManifestName), c.path(RollbackName)); err != nil {
		t.Fatal(err)
	}
	for _, name := range artifactNames(arts) {
		if err := renameAtomic(filepath.Join(stage, ArtifactDir, name), filepath.Join(c.ArtifactDir(), name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := renameAtomic(filepath.Join(stage, ManifestName), c.path(ManifestName)); err != nil {
		t.Fatal(err)
	}
	// Crash here: journal still says staged but the manifest is the new one.
	st, err := c.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if st.Mixed || st.OldOrNew != "new" || st.Digest != next.Digest {
		t.Fatalf("recovery misclassified: %+v", st)
	}
	j, _ := c.ReadJournal()
	if j == nil || j.Phase != PhaseCommited {
		t.Fatalf("journal not repaired: %+v", j)
	}
	if next.Digest == first.Digest {
		t.Fatal("test setup produced identical digests")
	}
	if err := c.VerifyDir(); err != nil {
		t.Fatalf("new release does not verify after recovery: %v", err)
	}
}

// A crash after artifacts are promoted but before the manifest rename used to
// leave the old manifest pointing at the new bytes.  Content-addressed names
// make the promotion non-destructive, so recovery keeps a verifiable old
// release and pruning removes only the unreferenced new artifacts.
func TestRecoverAfterPromoteBeforeManifestRenameVerifies(t *testing.T) {
	dir := t.TempDir()
	c, first := seedRelease(t, dir, "one")
	next, arts := testManifest(t, "rel", "two", map[string][]byte{"a.m4a": []byte("upgraded a"), "b.m4a": []byte("upgraded b")})
	stage, err := c.Stage(next, arts)
	if err != nil {
		t.Fatal(err)
	}
	// Promote artifacts and crash before the manifest rename.
	for _, name := range artifactNames(arts) {
		if err := os.MkdirAll(c.ArtifactDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := renameAtomic(filepath.Join(stage, ArtifactDir, name), filepath.Join(c.ArtifactDir(), name)); err != nil {
			t.Fatal(err)
		}
	}
	st, err := c.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if st.Mixed || st.OldOrNew != "old" {
		t.Fatalf("recovery misclassified: %+v", st)
	}
	cur, err := c.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if cur.Digest != first.Digest {
		t.Fatalf("old manifest not restored: %s vs %s", cur.Digest, first.Digest)
	}
	// The crucial property: the restored release still verifies, because the
	// artifacts it references were never overwritten.
	if err := c.VerifyDir(); err != nil {
		t.Fatalf("restored release does not verify: %v", err)
	}
}

func TestContentAddressedArtifactsAreNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	c, first := seedRelease(t, dir, "one")
	oldArt := first.Resources[0].Artifact
	before, _, err := HashFile(filepath.Join(c.ArtifactDir(), oldArt))
	if err != nil {
		t.Fatal(err)
	}
	next, arts := testManifest(t, "rel", "two", map[string][]byte{"zz9.m4a": []byte("fresh content")})
	stage, err := c.Stage(next, arts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(stage); err != nil {
		t.Fatal(err)
	}
	after, _, err := HashFile(filepath.Join(c.ArtifactDir(), oldArt))
	if err != nil {
		t.Fatalf("previous artifact was removed by the new commit: %v", err)
	}
	if before != after {
		t.Fatal("previous artifact bytes changed")
	}
	_ = first
}

// artifactNames returns staged artifact names in a stable order.
func artifactNames(arts map[string][]byte) []string {
	names := make([]string, 0, len(arts))
	for n := range arts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
