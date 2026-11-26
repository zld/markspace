package cache

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"mediatrace/internal/ebml"
)

func newSHA(t *testing.T) []byte {
	t.Helper()
	sum := make([]byte, 32)
	if _, err := rand.Read(sum); err != nil {
		t.Fatal(err)
	}
	return sum
}

func TestMissBuildHitCycle(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sha := newSHA(t)
	key := "deadbeef"

	_, state, err := s.Lookup(key, "/input/sample.webm", sha)
	if err != nil || state != StateMiss {
		t.Fatalf("first lookup: state=%s err=%v", state, err)
	}

	b, err := s.Builder(key, "/input/sample.webm", sha)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Append(ebml.IndexEntry{TrackNo: 1, TimeNs: 7}); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(key, b); err != nil {
		t.Fatal(err)
	}

	idx, state, err := s.Lookup(key, "/input/sample.webm", sha)
	if err != nil || state != StateHit {
		t.Fatalf("second lookup: state=%s err=%v", state, err)
	}
	defer idx.Close()
	e, err := idx.Entry(0)
	if err != nil || e.TimeNs != 7 {
		t.Fatalf("entry round trip wrong: %+v err=%v", e, err)
	}
}

func TestInterruptedBuildIsRebuiltNotHit(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	sha := newSHA(t)
	key := "cafe"

	b, err := s.Builder(key, "/input/sample.webm", sha)
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Append(ebml.IndexEntry{TrackNo: 1})
	// Simulate crash: builder temp closed without Finalize/Commit.
	_ = b.AbortForTest()

	_, state, err := s.Lookup(key, "/input/sample.webm", sha)
	if err != nil {
		t.Fatal(err)
	}
	if state != StateRebuilt {
		t.Fatalf("want rebuilt after interrupted build, got %s", state)
	}
	// The temp file must not be readable as a valid index.
	tmpPath := filepath.Join(s.root, tmpDirName, key+".mtid")
	if _, err := ebml.OpenIndex(tmpPath, sha); err == nil {
		t.Fatal("interrupted temp index opened successfully")
	}
}

func TestCorruptIndexIsRebuilt(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	sha := newSHA(t)
	key := "abcd"
	b, _ := s.Builder(key, "/input/sample.webm", sha)
	_ = b.Append(ebml.IndexEntry{TrackNo: 2})
	if err := s.Commit(key, b); err != nil {
		t.Fatal(err)
	}
	// Tamper with the published index.
	p := s.indexPath(key)
	f, _ := os.OpenFile(p, os.O_RDWR, 0o644)
	_, _ = f.WriteAt([]byte{0xFF, 0xFE, 0xFD}, 50)
	f.Close()

	_, state, err := s.Lookup(key, "/input/sample.webm", sha)
	if err != nil {
		t.Fatalf("lookup should report state, not hard error: %v", err)
	}
	if state != StateRebuilt {
		t.Fatalf("want rebuilt for corrupt index, got %s", state)
	}
}

func TestChangedInputIsRebuilt(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	sha1 := newSHA(t)
	key := "stable"
	b, _ := s.Builder(key, "/input/sample.webm", sha1)
	_ = b.Append(ebml.IndexEntry{})
	if err := s.Commit(key, b); err != nil {
		t.Fatal(err)
	}
	// Different content hash at the same key: mismatch must surface as a
	// rebuild signal plus the explicit mismatch error.
	sha2 := newSHA(t)
	_, state, err := s.Lookup(key, "/input/sample.webm", sha2)
	if err == nil || !errors.Is(err, ebml.ErrIndexMismatch) {
		t.Fatalf("want mismatch error, got state=%s err=%v", state, err)
	}
	if state != StateRebuilt {
		t.Fatalf("want rebuilt state for stale index, got %s", state)
	}
}

func TestRepairedFileAtSamePathIsRebuilt(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	inputPath := "/input/incident.webm"

	// First attempt with the corrupt content key fails before commit.
	badSHA := newSHA(t)
	badKey := "badcontent"
	b, err := s.Builder(badKey, inputPath, badSHA)
	if err != nil {
		t.Fatal(err)
	}
	_ = b.AbortForTest() // simulate the scan failing before publication

	// The repaired bytes have a new content key at the same input path.
	goodSHA := newSHA(t)
	goodKey := "goodcontent"
	_, state, err := s.Lookup(goodKey, inputPath, goodSHA)
	if err != nil {
		t.Fatal(err)
	}
	if state != StateRebuilt {
		t.Fatalf("repaired file at known path: want rebuilt, got %s", state)
	}

	// After a successful rebuild and rerun with identical bytes, it is a hit.
	b2, _ := s.Builder(goodKey, inputPath, goodSHA)
	_ = b2.Append(ebml.IndexEntry{TrackNo: 1})
	if err := s.Commit(goodKey, b2); err != nil {
		t.Fatal(err)
	}
	idx, state, err := s.Lookup(goodKey, inputPath, goodSHA)
	if err != nil || state != StateHit {
		t.Fatalf("rerun: state=%s err=%v", state, err)
	}
	idx.Close()
}

func TestDeterministicKey(t *testing.T) {
	r := stringReader("same bytes")
	k1, sum1, err := Key(r)
	if err != nil {
		t.Fatal(err)
	}
	k2, sum2, _ := Key(stringReader("same bytes"))
	if k1 != k2 {
		t.Fatal("same content produced different keys")
	}
	if len(k1) != 64 || string(sum1) != string(sum2) {
		t.Fatalf("key/sha wrong: len=%d", len(k1))
	}
	k3, _, _ := Key(stringReader("different bytes"))
	if k3 == k1 {
		t.Fatal("different content produced same key")
	}
}

type strReader string

func (s strReader) Read(p []byte) (int, error) {
	n := copy(p, s)
	if n < len(s) {
		return n, nil
	}
	return n, io.EOF
}

func stringReader(s string) strReader { return strReader(s) }
