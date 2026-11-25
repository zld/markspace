package cache

import (
	"testing"
)

func TestThreeHashesAreIndependent(t *testing.T) {
	payloads := [][]byte{{1, 2, 3}, {4, 5}}
	headers := [][]byte{{0x80, 0x60}, {0x80, 0xE0}}
	meta := []byte(`{"track":1,"rate":90000}`)

	content := ContentHash(payloads)
	container := ContainerHash("H264", headers)
	metadata := MetadataHash(meta)
	for _, h := range []string{content, container, metadata} {
		if len(h) < len(PrefixContent)+32 {
			t.Fatalf("hash too short: %s", h)
		}
	}
	if content == container || container == metadata || content == metadata {
		t.Fatal("the three identity hashes must differ")
	}
	// Same payload bytes, different path/labels: content hash stable.
	if ContentHash([][]byte{{1, 2, 3}, {4, 5}}) != content {
		t.Fatal("content hash must depend on payload bytes only")
	}
	// Changed payload bytes: content hash changes.
	if ContentHash([][]byte{{1, 2, 3}, {4, 6}}) == content {
		t.Fatal("replaced content must produce a new content hash")
	}
	// Same payload, new container headers: container hash changes while
	// content hash stays.
	if ContainerHash("H265", headers) == container {
		t.Fatal("container change must change container hash")
	}
	if MetadataHash([]byte(`{"track":2}`)) == metadata {
		t.Fatal("metadata change must change metadata hash")
	}
}

func TestContentCacheInvalidation(t *testing.T) {
	c := NewContentCache()
	key := ContentHash([][]byte{{9, 9}})
	c.Put(key, []byte("reassembled-sample"))

	if v, ok := c.Lookup(key); !ok || string(v) != "reassembled-sample" {
		t.Fatal("first lookup must hit")
	}
	if st := c.Stats(); st.Hits != 1 || st.Misses != 0 {
		t.Fatalf("stats after hit wrong: %+v", st)
	}
	if _, ok := c.Lookup("sha256:content:absent"); ok {
		t.Fatal("unknown key must miss")
	}
	if !c.Invalidate(key) || c.Has(key) {
		t.Fatal("invalidation must evict the entry")
	}
	if c.Invalidate(key) {
		t.Fatal("second invalidation of same key reports false")
	}
	if st := c.Stats(); st.Invalidations != 1 || st.Misses != 1 {
		t.Fatalf("stats after invalidate wrong: %+v", st)
	}
}

func TestContentCacheDefensiveCopy(t *testing.T) {
	c := NewContentCache()
	key := "k"
	val := []byte{1, 2}
	c.Put(key, val)
	val[0] = 99
	got, _ := c.Lookup(key)
	if got[0] != 1 {
		t.Fatal("cache must not alias caller slices")
	}
}
