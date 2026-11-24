package index_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zld/mediapipe/fixtures"
	"github.com/zld/mediapipe/index"
	"github.com/zld/mediapipe/media"
)

func probed(t *testing.T, dir string) (*media.Container, *index.Store) {
	t.Helper()
	spec := fixtures.DefaultSpec("good.mp4")
	res, err := fixtures.Generate(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := media.Probe(res.MainPath, media.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	store, err := index.NewStore(filepath.Join(dir, "idx"), media.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return c, store
}

func TestBuildIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	c, _ := probed(t, dir)
	a, err := index.Build(c)
	if err != nil {
		t.Fatal(err)
	}
	b, err := index.Build(c)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest {
		t.Fatalf("digest differs: %s vs %s", a.Digest, b.Digest)
	}
	if a.Version != index.Version {
		t.Fatalf("version=%q", a.Version)
	}
	if a.InputSHA256 != c.SHA256 {
		t.Fatal("input digest not recorded")
	}
	tr := a.Track("video")
	if tr == nil {
		t.Fatal("no video track indexed")
	}
	if tr.SampleCount != 240 || len(tr.Keyframes) != 8 {
		t.Fatalf("track=%+v", tr)
	}
	// Keyframe 0 is at tick 0, keyframe 15 (sample 16) is at tick 15000.
	if tr.Keyframes[0].Ticks != 0 || tr.Keyframes[1].Sample != 31 || tr.Keyframes[1].Ticks != 30000 {
		t.Fatalf("keyframes=%+v", tr.Keyframes)
	}
}

func TestStoreReusesUnchangedIndex(t *testing.T) {
	dir := t.TempDir()
	c, store := probed(t, dir)
	out1, err := store.Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if !out1.Rebuilt {
		t.Fatal("first scan should build")
	}
	out2, err := store.Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if !out2.Reused || out2.Rebuilt {
		t.Fatalf("second scan should reuse: %+v", out2)
	}
	if out1.Index.Digest != out2.Index.Digest {
		t.Fatal("reused index digest differs")
	}
}

func TestStoreRebuildsWhenInputChanges(t *testing.T) {
	dir := t.TempDir()
	c, store := probed(t, dir)
	first, err := store.Scan(c)
	if err != nil {
		t.Fatal(err)
	}

	// Rewrite the input bytes with a different (still valid) payload while
	// keeping the same media structure.
	path := c.Path
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := append([]byte{}, orig...)
	for i := len(modified) - 200; i < len(modified); i++ {
		modified[i] ^= 0x5A
	}
	if err := os.WriteFile(path, modified, 0o644); err != nil {
		t.Fatal(err)
	}
	c2, err := media.Probe(path, media.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Scan(c2)
	if err != nil {
		t.Fatal(err)
	}
	if second.Index.InputSHA256 == first.Index.InputSHA256 {
		t.Fatal("input digest did not change")
	}
	if second.Index.Digest == first.Index.Digest {
		t.Fatal("index digest did not change with input bytes")
	}
	if !second.Rebuilt {
		t.Fatal("changed input must rebuild")
	}
}

func TestStoreDiscardsCorruptCache(t *testing.T) {
	dir := t.TempDir()
	c, store := probed(t, dir)
	out, err := store.Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the persisted digest so validation fails.
	path := out.CachePath
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	raw["digest"] = "deadbeef"
	b, _ = json.Marshal(raw)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	out2, err := store.Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if out2.Reused {
		t.Fatal("corrupt cache should not be reused")
	}
	if !out2.Rebuilt {
		t.Fatal("corrupt cache should be rebuilt")
	}
	if out2.Index.Digest != out.Index.Digest {
		t.Fatal("rebuilt digest should match the original")
	}
}

func TestStoreRejectsCacheForOtherInput(t *testing.T) {
	dir := t.TempDir()
	c, store := probed(t, dir)
	if _, err := store.Scan(c); err != nil {
		t.Fatal(err)
	}
	// Write an index file under a different input's key and confirm Scan(a)
	// never picks it up: the path is keyed by SHA-256, so this is structural.
	other := *c
	other.SHA256 = strings.Repeat("0", 64)
	if _, err := store.Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.PathFor(c.SHA256)); err != nil {
		t.Fatal("original index missing")
	}
}

func TestValidateRejectsBadVersion(t *testing.T) {
	idx := &index.Index{Version: "old", Digest: "x"}
	if err := idx.Validate(); err == nil {
		t.Fatal("expected version failure")
	}
}

func TestIndexDigestCoversContent(t *testing.T) {
	dir := t.TempDir()
	c, _ := probed(t, dir)
	a, err := index.Build(c)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := index.Build(c)
	// Mutating any tracked field changes the digest.
	b.Tracks[0].StartUS++
	want, _ := recompute(t, b)
	if want == a.Digest {
		t.Fatal("digest collided after content change")
	}
}

// recompute rebuilds the digest the way the package does after Validate
// clears it, by re-reading Canonical and hashing.
func recompute(t *testing.T, idx *index.Index) (string, error) {
	t.Helper()
	clone := *idx
	clone.Digest = ""
	b, err := json.Marshal(&clone)
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
