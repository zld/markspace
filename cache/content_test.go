package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func key(block int) Key {
	return Key{
		Source: "valid.mp4",
		Input:  "abc123",
		Block:  block,
		Params: Params{SampleRate: 48000, Layout: "stereo", LoudnessLUFS: "-16", GainDB: "-3.2"}.Key(),
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	s := testStore(t)
	payload := []byte("pcm block bytes")
	e, err := s.Put(key(3), payload)
	if err != nil {
		t.Fatal(err)
	}
	if e.OutputDigest != DigestOf(payload) {
		t.Fatalf("digest %s", e.OutputDigest)
	}
	got, ok, err := s.Get(key(3))
	if err != nil || !ok {
		t.Fatalf("get: ok=%t err=%v", ok, err)
	}
	if string(got.Payload) != string(payload) {
		t.Fatalf("payload mismatch")
	}
	if got.OutputDigest != e.OutputDigest {
		t.Fatal("digest changed across storage")
	}
	st := s.Stats()
	if st.Hits != 1 || st.Stored != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestMissCounts(t *testing.T) {
	s := testStore(t)
	if _, ok, err := s.Get(key(9)); ok || err != nil {
		t.Fatalf("expected miss, got ok=%t err=%v", ok, err)
	}
	if s.Stats().Misses != 1 {
		t.Fatalf("misses %d", s.Stats().Misses)
	}
}

func TestParamsChangeMisses(t *testing.T) {
	s := testStore(t)
	if _, err := s.Put(key(1), []byte("x")); err != nil {
		t.Fatal(err)
	}
	other := key(1)
	other.Params = Params{SampleRate: 44100, Layout: "stereo", LoudnessLUFS: "-16", GainDB: "-3.2"}.Key()
	if _, ok, _ := s.Get(other); ok {
		t.Fatal("different parameters must not hit")
	}
}

func TestSourceChangeMisses(t *testing.T) {
	s := testStore(t)
	if _, err := s.Put(key(1), []byte("x")); err != nil {
		t.Fatal(err)
	}
	other := key(1)
	other.Source = "corrupt.mp4"
	if _, ok, _ := s.Get(other); ok {
		t.Fatal("different source must not hit")
	}
}

func TestTamperedPayloadRejected(t *testing.T) {
	s := testStore(t)
	if _, err := s.Put(key(2), []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := s.Tamper(key(2)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get(key(2)); ok || err != nil {
		t.Fatalf("tampered entry must be rejected silently, got ok=%t err=%v", ok, err)
	}
	st := s.Stats()
	if st.Rejected != 1 || st.Recompute != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestMalformedEntryRejected(t *testing.T) {
	s := testStore(t)
	if _, err := s.Put(key(4), []byte("payload")); err != nil {
		t.Fatal(err)
	}
	// Corrupt the file format entirely.
	p := s.path(key(4).Digest())
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get(key(4)); ok || err != nil {
		t.Fatalf("malformed entry must be rejected, got ok=%t err=%v", ok, err)
	}
	if s.Stats().Rejected != 1 {
		t.Fatalf("stats %+v", s.Stats())
	}
}

func TestParamsNormalize(t *testing.T) {
	a := Params{SampleRate: 48000, Layout: " stereo ", LoudnessLUFS: "-16.0", GainDB: "0"}
	b := Params{SampleRate: 48000, Layout: "stereo", LoudnessLUFS: "-16.0", GainDB: "0"}
	if a.Key() != b.Key() {
		t.Fatalf("normalisation mismatch:\n%s\n%s", a.Key(), b.Key())
	}
	c := Params{SampleRate: 48000, Layout: "mono", LoudnessLUFS: "-16.0", GainDB: "0"}
	if a.Key() == c.Key() {
		t.Fatal("layout must affect the key")
	}
}

func TestPutRejectsEmpty(t *testing.T) {
	s := testStore(t)
	if _, err := s.Put(key(1), nil); err == nil {
		t.Fatal("empty payload must be rejected")
	}
}

func TestKeysStable(t *testing.T) {
	s := testStore(t)
	for i := 1; i <= 3; i++ {
		if _, err := s.Put(key(i), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := s.Keys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 {
		t.Fatalf("keys %v", keys)
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] > keys[i] {
			t.Fatalf("keys not sorted: %v", keys)
		}
	}
}

func TestOpenRequiresDir(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("empty dir must fail")
	}
}

func TestEntryPathFanout(t *testing.T) {
	s := testStore(t)
	k := key(1)
	p := s.path(k.Digest())
	if filepath.Base(filepath.Dir(p)) != k.Digest()[:2] {
		t.Fatalf("unexpected fanout path %s", p)
	}
}
