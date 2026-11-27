package rollback

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/assetpack/internal/manifest"
)

// artifactName is the content-addressed name used by the pipeline.
func artifactName(data []byte) string { return sumHex(data) + ".m4a" }

// testManifest builds a v2 manifest whose artifacts are content-addressed.
// contents maps logical resource names (without extension) to bytes; the map
// actually staged is returned so callers do not have to recompute names.
func testManifest(t *testing.T, release, tag string, contents map[string][]byte) (*manifest.V2, map[string][]byte) {
	t.Helper()
	m := &manifest.V2{
		Schema: manifest.SchemaV2, Release: release, Sequence: 1,
		Target: manifest.TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16},
	}
	staged := make(map[string][]byte, len(contents))
	names := make([]string, 0, len(contents))
	for n := range contents {
		names = append(names, n)
	}
	for _, n := range names {
		data := contents[n]
		name := artifactName(data)
		staged[name] = data
		m.Resources = append(m.Resources, manifest.Resource{
			Name: strings.TrimSuffix(n, ".m4a"), Kind: "audio", SourceVersion: 1,
			SourceSHA256: strings.Repeat("a", 64), SHA256: sumHex(data),
			Bytes: int64(len(data)), Artifact: name, Rate: 48000, Layout: "stereo",
			Frames: 480, LoudnessLUFS: -16, Peak: 0.5,
			Media: []manifest.Interval{{StartSample: 0, LengthSamples: 480}},
		})
	}
	if _, err := m.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	return m, staged
}

func sumHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func seedRelease(t *testing.T, dir, tag string) (*Checkpoint, *manifest.V2) {
	t.Helper()
	c, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string][]byte{"a.m4a": []byte("old a " + tag), "b.m4a": []byte("old b " + tag)}
	m, arts := testManifest(t, "rel", tag, contents)
	stage, err := c.Stage(m, arts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(stage); err != nil {
		t.Fatal(err)
	}
	committed, err := c.ReadManifest()
	if err != nil || committed == nil {
		t.Fatalf("seed failed: %v", err)
	}
	return c, committed
}

func TestStageCommitRecoverClean(t *testing.T) {
	dir := t.TempDir()
	c, committed := seedRelease(t, dir, "one")
	if !c.ManifestExists() {
		t.Fatal("manifest not committed")
	}
	st, err := c.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if st.OldOrNew != "new" || st.Mixed || st.Digest != committed.Digest {
		t.Fatalf("unexpected recovery state: %+v", st)
	}
	if err := c.VerifyDir(); err != nil {
		t.Fatalf("committed release does not verify: %v", err)
	}
}

func TestStageRejectsDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	c, _ := New(dir)
	m, arts := testManifest(t, "rel", "x", map[string][]byte{"a.m4a": []byte("payload")})
	for k := range arts {
		arts[k] = []byte("different")
	}
	if _, err := c.Stage(m, arts); err == nil {
		t.Fatal("stage accepted mismatched bytes")
	}
}

func TestStageRejectsMissingArtifact(t *testing.T) {
	dir := t.TempDir()
	c, _ := New(dir)
	m, _ := testManifest(t, "rel", "x", map[string][]byte{"a.m4a": []byte("payload")})
	if _, err := c.Stage(m, map[string][]byte{}); err == nil {
		t.Fatal("stage accepted a missing artifact")
	}
}

func TestRollbackRestoresPrevious(t *testing.T) {
	dir := t.TempDir()
	c, first := seedRelease(t, dir, "one")
	// Stage and commit a second release with different bytes.
	next, arts := testManifest(t, "rel", "two", map[string][]byte{"a.m4a": []byte("new a"), "b.m4a": []byte("new b")})
	stage, err := c.Stage(next, arts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(stage); err != nil {
		t.Fatal(err)
	}
	second, _ := c.ReadManifest()
	if second.Digest == first.Digest {
		t.Fatal("second release did not change the digest")
	}
	// Roll back explicitly.
	if err := c.Rollback(); err != nil {
		t.Fatal(err)
	}
	restored, err := c.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if restored.Digest != first.Digest {
		t.Fatalf("rollback digest %s, want %s", restored.Digest, first.Digest)
	}
	st, _ := c.Recover()
	if st.OldOrNew != "old" || st.Mixed {
		t.Fatalf("recovery after rollback: %+v", st)
	}
}

func TestRollbackWithoutJournalFails(t *testing.T) {
	c, _ := New(t.TempDir())
	if err := c.Rollback(); err == nil {
		t.Fatal("rollback without a journal succeeded")
	}
}

func TestArtifactVerifyDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	c, _ := seedRelease(t, dir, "one")
	m, err := c.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.ArtifactDir(), m.Resources[0].Artifact), []byte("tampered!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.VerifyDir(); err == nil {
		t.Fatal("tampered artifact verified")
	}
}

func TestNoTempFilesLeftBehind(t *testing.T) {
	dir := t.TempDir()
	seedRelease(t, dir, "one")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), TempSuffix) {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
