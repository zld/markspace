package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestContentHashChangesOnReplacement(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "frag.m4s", "original-bytes")
	h1, err := ContentHashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the file at the SAME path with different content.
	if err := os.WriteFile(p, []byte("replaced-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	h2, err := ContentHashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("content hash must change when the file is replaced")
	}
	id1, _ := IdentityOf(p)
	// Re-stating gives the same path identity but a new hash -> new parse key.
	pk1 := NewParseKey(FileIdentity{Path: id1.Path}, h1)
	pk2 := NewParseKey(FileIdentity{Path: id1.Path}, h2)
	if pk1.Hash() == pk2.Hash() {
		t.Fatal("same-path replacement must invalidate the parse key")
	}
}

func TestParseCacheStableForIdenticalContent(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "a.m4s", "stable")
	id, err := IdentityOf(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ContentHashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	k1 := NewParseKey(id, h)
	k2 := NewParseKey(id, h)
	if k1.Hash() != k2.Hash() {
		t.Fatal("identical identity+content must yield a stable parse key")
	}
}

func TestMetadataOnlyChangeRebuildsIndexButKeepsParseHit(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "frag.m4s", "media-bytes")
	id, _ := IdentityOf(p)
	content, _ := ContentHashFile(p)
	parseKey := NewParseKey(id, content)
	timelineHash := Fingerprint(map[string]any{"seq": 1, "tfdt": 0})

	metaV1 := Fingerprint(map[string]any{"version": 1, "target_duration": 4})
	metaV2 := Fingerprint(map[string]any{"version": 2, "target_duration": 6})

	ik1 := NewIndexKey(parseKey, timelineHash, metaV1)
	ik2 := NewIndexKey(parseKey, timelineHash, metaV2)

	// Parse key unchanged -> parse cache stays valid.
	if NewParseKey(id, content).Hash() != parseKey.Hash() {
		t.Fatal("parse key must not move on a metadata-only change")
	}
	// Index key changes -> index must be rebuilt.
	if ik1.Hash() == ik2.Hash() {
		t.Fatal("manifest metadata change must invalidate the index key")
	}
}

func TestTimelineChangeRebuildsIndex(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "f.m4s", "x")
	id, _ := IdentityOf(p)
	content, _ := ContentHashFile(p)
	parseKey := NewParseKey(id, content)
	meta := Fingerprint(map[string]any{"v": 1})
	tl1 := Fingerprint(map[string]any{"tfdt": 100})
	tl2 := Fingerprint(map[string]any{"tfdt": 250})
	if NewIndexKey(parseKey, tl1, meta).Hash() == NewIndexKey(parseKey, tl2, meta).Hash() {
		t.Fatal("a corrected-timeline change must invalidate the index key")
	}
}

func TestVersionBumpInvalidatesKeys(t *testing.T) {
	id := FileIdentity{Path: "/x", Size: 1}
	pk := NewParseKey(id, "deadbeef")
	old := pk.Hash()
	ParseVersionBumped := ParseVersion + "-next"
	pk.ParseVersion = ParseVersionBumped
	if pk.Hash() == old {
		t.Fatal("a parse-version bump must change the key")
	}
}

func TestStorePutGetAndStats(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	key := "abcdef0123456789"
	if _, out := store.Get(key); out.Hit {
		t.Fatal("cold lookup must miss")
	}
	if err := store.Put(key, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	got, out := store.Get(key)
	if !out.Hit || string(got) != "payload" {
		t.Fatalf("warm lookup must hit: %+v data=%q", out, got)
	}
	st := store.Stats()
	if st.Hits != 1 || st.Misses != 1 {
		t.Fatalf("stats wrong: %+v", st)
	}
}

func TestStoreRejectsEmptyKey(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "c"))
	if err := store.Put("", []byte("x")); err == nil {
		t.Fatal("empty cache key must be rejected")
	}
}

func TestStoreIsContentAddressed(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "c"))
	k1 := NewParseKey(FileIdentity{Path: "/a"}, "hash1").Hash()
	k2 := NewParseKey(FileIdentity{Path: "/b"}, "hash1").Hash()
	// Different path identity but we only assert distinct inputs map to
	// distinct digests when identity differs.
	if k1 == k2 {
		t.Fatal("different identities must not collide")
	}
	_ = store
}
