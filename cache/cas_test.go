package cache

import (
	"strings"
	"testing"

	"mediamux/media"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func prov(node, id string, idx int, length int64) Provenance {
	return Provenance{Node: node, SegmentID: id, SegmentIdx: idx, InputLength: length, ManifestVer: "v1"}
}

func TestKeyIsContentAddress(t *testing.T) {
	a := Key([]byte("segment-a"))
	b := Key([]byte("segment-b"))
	if a == b || len(a) != 64 {
		t.Fatalf("bad keys %q %q", a, b)
	}
	if Key([]byte("segment-a")) != a {
		t.Fatal("key must be deterministic")
	}
}

func TestPutThenVerifiedHit(t *testing.T) {
	s := newStore(t)
	data := []byte("fragment-bytes")
	p := prov("edge-a", "seg-0", 0, int64(len(data)))
	if _, err := s.Put(data, p); err != nil {
		t.Fatal(err)
	}
	res := s.Lookup(Key(data), p)
	if !res.Hit || res.Source != "cache" {
		t.Fatalf("expected verified hit, got %+v", res)
	}
	st := s.Stats()
	if st.Hits != 1 || st.SourceRejects != 0 || st.UsedBytes != int64(len(data)) {
		t.Fatalf("stats=%+v", st)
	}
}

func TestForgedSourceIsRejectedAndQuarantined(t *testing.T) {
	s := newStore(t)
	data := []byte("fragment-bytes")
	origin := prov("edge-a", "seg-0", 0, int64(len(data)))
	if _, err := s.Put(data, origin); err != nil {
		t.Fatal(err)
	}
	// Recovery asks for the same key but records node edge-b: the cached
	// entry claims to come from somewhere else and must not be trusted.
	forged := prov("edge-b", "seg-0", 0, int64(len(data)))
	res := s.Lookup(Key(data), forged)
	if res.Hit {
		t.Fatal("forged provenance must not produce a hit")
	}
	if res.Source != "quarantine" || res.Rejection == nil {
		t.Fatalf("res=%+v", res)
	}
	if res.Rejection.ExpectedNode != "edge-b" || res.Rejection.RecordedNode != "edge-a" {
		t.Fatalf("rejection=%+v", res.Rejection)
	}
	if !strings.Contains(res.Reason, "source node") {
		t.Fatalf("reason=%q", res.Reason)
	}
	if got := s.Quarantined(Key(data)); len(got) != 1 || !got[0].Quarantined {
		t.Fatalf("quarantine=%+v", got)
	}
	st := s.Stats()
	if st.SourceRejects != 1 || st.Rebuilt != 1 || st.Entries != 0 || st.UsedBytes != 0 {
		t.Fatalf("stats=%+v", st)
	}
}

func TestLengthMismatchIsRejected(t *testing.T) {
	s := newStore(t)
	data := []byte("1234567890")
	p := prov("edge-a", "seg-1", 1, int64(len(data)))
	if _, err := s.Put(data, p); err != nil {
		t.Fatal(err)
	}
	res := s.Lookup(Key(data), prov("edge-a", "seg-1", 1, 99))
	if res.Hit || res.Rejection == nil || res.Rejection.ExpectedLength != 99 {
		t.Fatalf("res=%+v", res)
	}
	if !strings.Contains(res.Reason, "input length") {
		t.Fatalf("reason=%q", res.Reason)
	}
}

func TestDigestMismatchIsRejected(t *testing.T) {
	s := newStore(t)
	data := []byte("payload")
	p := prov("edge-a", "seg-2", 2, int64(len(data)))
	if _, err := s.Put(data, p); err != nil {
		t.Fatal(err)
	}
	// Ask for the correct key but claim a different digest: the recorded
	// content no longer matches what the caller expects.
	expect := p
	expect.Digest = Key([]byte("something else"))
	res := s.Lookup(Key(data), expect)
	if res.Hit {
		t.Fatal("digest mismatch must not hit")
	}
	if !strings.Contains(res.Reason, "digest") {
		t.Fatalf("reason=%q", res.Reason)
	}
}

func TestPutRejectsForgedProvenanceDigest(t *testing.T) {
	s := newStore(t)
	p := prov("edge-a", "seg-0", 0, 4)
	p.Digest = Key([]byte("different"))
	_, err := s.Put([]byte("data"), p)
	if err == nil || !media.HasCode(err, media.CodeCachePoison) {
		t.Fatalf("err=%v", err)
	}
}

func TestRebuildReplacesRejectedEntry(t *testing.T) {
	s := newStore(t)
	data := []byte("original")
	if _, err := s.Put(data, prov("edge-a", "seg-0", 0, int64(len(data)))); err != nil {
		t.Fatal(err)
	}
	_ = s.Lookup(Key(data), prov("edge-c", "seg-0", 0, int64(len(data))))
	fresh := []byte("rebuilt-data")
	if _, err := s.Rebuild(fresh, prov("edge-c", "seg-0", 0, int64(len(fresh)))); err != nil {
		t.Fatal(err)
	}
	res := s.Lookup(Key(fresh), prov("edge-c", "seg-0", 0, int64(len(fresh))))
	if !res.Hit {
		t.Fatalf("rebuilt entry not reusable: %+v", res)
	}
	if s.Stats().Rebuilt < 2 {
		t.Fatalf("rebuild not recorded: %+v", s.Stats())
	}
}

func TestCapacityIsEnforced(t *testing.T) {
	s, err := NewStore(10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put([]byte("0123456789ab"), prov("a", "s", 0, 12)); err == nil {
		t.Fatal("capacity overflow must fail")
	}
	if s.Stats().UsedBytes != 0 {
		t.Fatalf("failed put consumed capacity: %+v", s.Stats())
	}
}

func TestIdempotentPut(t *testing.T) {
	s := newStore(t)
	data := []byte("same")
	p := prov("edge-a", "s", 0, int64(len(data)))
	if _, err := s.Put(data, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(data, p); err != nil {
		t.Fatal(err)
	}
	if s.Stats().UsedBytes != int64(len(data)) || s.Stats().Entries != 1 {
		t.Fatalf("duplicate storage: %+v", s.Stats())
	}
}

func TestWriteManifestIsAtomicAndStable(t *testing.T) {
	dir := t.TempDir()
	p := prov("edge-a", "seg-0", 0, 5)
	p.Digest = Key([]byte("hello"))
	path, err := p.WriteManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, p.Digest+".json") {
		t.Fatalf("path=%q", path)
	}
	again, err := p.WriteManifest(dir)
	if err != nil || again != path {
		t.Fatalf("unstable path %q err=%v", again, err)
	}
}
