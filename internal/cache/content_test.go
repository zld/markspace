package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func testKey(t *testing.T, src []byte) (Key, [32]byte) {
	t.Helper()
	sum := sha256.Sum256(src)
	return NewKey(KeyInput{
		Kind:       "fragment",
		Source:     "a",
		SourcePath: "a.m4s",
		SourceSum:  sum,
		Params:     Params{MediaRateNum: 720000, MediaRateDen: 1, VideoTrackID: 1, AudioTrackID: 2, Repair: true},
	}), sum
}

func TestCachePutGetRoundTrip(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, sum := testKey(t, []byte("origin"))
	payload := []byte("artifact-bytes")
	if _, err := c.Put(key, sum, payload); err != nil {
		t.Fatal(err)
	}
	res, err := c.Get(key, sum)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry == nil {
		t.Fatalf("miss: %s", res.Reason)
	}
	if string(res.Entry.Payload) != string(payload) {
		t.Fatalf("payload = %q", res.Entry.Payload)
	}
}

func TestCacheSourceDigestChangeIsMissAndRecomputes(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, sum := testKey(t, []byte("origin-v1"))
	if _, err := c.Put(key, sum, []byte("v1-artifact")); err != nil {
		t.Fatal(err)
	}
	// The origin content changes; the key is the same but the digest is not.
	changed := sha256.Sum256([]byte("origin-v2"))
	res, err := c.Get(key, changed)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry != nil {
		t.Fatal("expected the changed source to miss")
	}
	if res.Reason != "source digest changed" {
		t.Fatalf("reason = %q", res.Reason)
	}
	// Recompute and store under the same key with the new digest: Put must
	// accept it only when the key's digest matches.
	if _, err := c.Put(key, changed, []byte("v2-artifact")); err == nil {
		t.Fatal("Put accepted a key whose digest does not match the origin")
	}
}

func TestCacheCorruptPayloadDetected(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, sum := testKey(t, []byte("origin"))
	if _, err := c.Put(key, sum, []byte("good-artifact")); err != nil {
		t.Fatal(err)
	}
	// Corrupt the payload but keep the metadata's recorded digest.
	path := filepath.Join(c.Dir(), "content", key.String()+".bin")
	if err := os.WriteFile(path, []byte("evil-artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := c.Get(key, sum)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry != nil {
		t.Fatal("corrupt payload was accepted")
	}
	if res.Reason != "content digest mismatch" {
		t.Fatalf("reason = %q", res.Reason)
	}
	if err := c.Invalidate(key); err != nil {
		t.Fatal(err)
	}
	if res, _ := c.Get(key, sum); res.Entry != nil {
		t.Fatal("invalidated entry came back")
	}
}

func TestCacheCorruptMetadataIsMiss(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, sum := testKey(t, []byte("origin"))
	if _, err := c.Put(key, sum, []byte("artifact")); err != nil {
		t.Fatal(err)
	}
	meta := filepath.Join(c.Dir(), "meta", key.String()+".json")
	if err := os.WriteFile(meta, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := c.Get(key, sum)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry != nil || res.Reason == "" {
		t.Fatalf("corrupt metadata result = %+v", res)
	}
}

func TestCacheTruncatedPayloadIsMiss(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, sum := testKey(t, []byte("origin"))
	if _, err := c.Put(key, sum, make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.Dir(), "content", key.String()+".bin")
	if err := os.Truncate(path, 50); err != nil {
		t.Fatal(err)
	}
	res, err := c.Get(key, sum)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry != nil {
		t.Fatal("truncated payload was accepted")
	}
}

func TestKeyIncludesParamsAndVersion(t *testing.T) {
	src := []byte("origin")
	sum := sha256.Sum256(src)
	a := NewKey(KeyInput{Kind: "f", Source: "a", SourcePath: "a", SourceSum: sum, Params: Params{Repair: true}})
	b := NewKey(KeyInput{Kind: "f", Source: "a", SourcePath: "a", SourceSum: sum, Params: Params{Repair: false}})
	if a.String() == b.String() {
		t.Fatal("params did not affect the key")
	}
	c := NewKey(KeyInput{Kind: "f", Source: "a", SourcePath: "a", SourceSum: sum, Params: Params{Repair: true}})
	if a.String() != c.String() {
		t.Fatal("identical inputs produced different keys")
	}
	// Digest is 64 hex characters.
	if len(a.String()) != 64 {
		t.Fatalf("key length = %d", len(a.String()))
	}
	if _, err := hex.DecodeString(a.String()); err != nil {
		t.Fatal(err)
	}
}
