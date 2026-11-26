package cas

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeyDeterminismAndSensitivity(t *testing.T) {
	k1 := Key{SourceDigest: "abc", NormVersion: NormalizationVersion,
		Params: map[string]string{"b": "2", "a": "1"}}
	k2 := Key{SourceDigest: "abc", NormVersion: NormalizationVersion,
		Params: map[string]string{"a": "1", "b": "2"}}
	if k1.String() != k2.String() {
		t.Fatal("parameter map order must not affect the key")
	}
	k3 := Key{SourceDigest: "abd", NormVersion: NormalizationVersion, Params: map[string]string{"a": "1", "b": "2"}}
	if k1.String() == k3.String() {
		t.Fatal("a changed source digest must change the key")
	}
	k4 := Key{SourceDigest: "abc", NormVersion: "norm-2", Params: map[string]string{"a": "1", "b": "2"}}
	if k1.String() == k4.String() {
		t.Fatal("a changed normalization version must change the key")
	}
	k5 := Key{SourceDigest: "abc", NormVersion: NormalizationVersion, Params: map[string]string{"a": "9", "b": "2"}}
	if k1.String() == k5.String() {
		t.Fatal("changed parameters must change the key")
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	k := Key{SourceDigest: "deadbeef", NormVersion: NormalizationVersion,
		Params: map[string]string{"track": "en"}}
	entry, err := st.Put(k, []byte("payload-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	got, payload, err := st.Get(k)
	if err != nil || string(payload) != "payload-bytes" {
		t.Fatalf("Get failed: %v payload=%q", err, payload)
	}
	if got.PayloadHash != entry.PayloadHash || got.Size != 13 {
		t.Fatalf("entry mismatch: %+v vs %+v", got, entry)
	}
	// Miss returns nil, not an error.
	if e, _, err := st.Get(Key{SourceDigest: "missing", NormVersion: NormalizationVersion}); err != nil || e != nil {
		t.Fatalf("cache miss must be (nil,nil): e=%v err=%v", e, err)
	}
}

func TestDigestTamperDetected(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	k := Key{SourceDigest: "s", NormVersion: NormalizationVersion}
	if _, err := st.Put(k, []byte("original")); err != nil {
		t.Fatal(err)
	}
	// Tamper with the blob on disk.
	key := k.String()
	if err := os.WriteFile(filepath.Join(dir, key+".blob"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Get(k); err == nil {
		t.Fatal("tampered payload must fail digest verification")
	}
}

func TestInvalidateOnlyAffectedSource(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	ka := Key{SourceDigest: "srcA", NormVersion: NormalizationVersion, Params: map[string]string{"p": "1"}}
	kb := Key{SourceDigest: "srcB", NormVersion: NormalizationVersion, Params: map[string]string{"p": "1"}}
	if _, err := st.Put(ka, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(kb, []byte("b")); err != nil {
		t.Fatal(err)
	}
	n, err := st.InvalidateSource("srcA")
	if err != nil || n != 1 {
		t.Fatalf("invalidate = %d, %v; want 1", n, err)
	}
	if e, _, _ := st.Get(ka); e != nil {
		t.Fatal("srcA derivative must be gone")
	}
	if e, _, err := st.Get(kb); err != nil || e == nil {
		t.Fatalf("srcB derivative must survive: e=%v err=%v", e, err)
	}
	// Invalidating again is a no-op without error.
	if n, err := st.InvalidateSource("srcA"); err != nil || n != 0 {
		t.Fatalf("re-invalidate = %d, %v; want 0", n, err)
	}
}

func TestDigestFileStreaming(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(path, []byte("hash-me"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := DigestBytes([]byte("hash-me")); got != want {
		t.Fatalf("DigestFile = %s, want %s", got, want)
	}
	if _, err := DigestFile(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("missing file must produce an error")
	}
}
