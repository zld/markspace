package checkpoint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/local/tracecheck/internal/clock"
)

func TestCleanStartWhenMissing(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Load("input-1")
	if err != nil {
		t.Fatal(err)
	}
	if s.NextIndex(st) != 0 || st.InputID != "input-1" {
		t.Fatalf("unexpected clean state: %+v", st)
	}
}

func TestAdvanceAndReload(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	st, _ := s.Load("input-1")
	if err := s.Advance(st, SegmentRecord{Index: 0, StartNS: 0, EndNS: 100, Digest: "d0", CacheKey: "k0"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Advance(st, SegmentRecord{Index: 1, StartNS: 100, EndNS: 200, Digest: "d1", CacheKey: "k1"}); err != nil {
		t.Fatal(err)
	}
	s2, _ := OpenStore(dir)
	loaded, err := s2.Load("input-1")
	if err != nil {
		t.Fatal(err)
	}
	if s2.NextIndex(loaded) != 2 || loaded.Segments[1].Digest != "d1" {
		t.Fatalf("reloaded state wrong: %+v", loaded)
	}
	if err := s.Finalize(loaded, "manifest-abc"); err != nil {
		t.Fatal(err)
	}
	again, _ := s2.Load("input-1")
	if again.ManifestDigest != "manifest-abc" {
		t.Fatalf("manifest digest not persisted: %+v", again)
	}
}

func TestNonContiguousAdvanceRejected(t *testing.T) {
	s, _ := OpenStore(t.TempDir())
	st, _ := s.Load("in")
	err := s.Advance(st, SegmentRecord{Index: 3})
	if clock.AsTraceError(err) == nil || clock.AsTraceError(err).Code != clock.CodeCheckpoint {
		t.Fatalf("expected checkpoint error, got %v", err)
	}
}

func TestTamperedChecksumRejected(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	st, _ := s.Load("in")
	if err := s.Advance(st, SegmentRecord{Index: 0, Digest: "d0"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, stateFileName)
	raw, _ := os.ReadFile(path)
	// Flip a byte inside the digest payload but keep valid JSON shape.
	tampered := append([]byte(nil), raw...)
	for i, b := range tampered {
		if b == 'd' && i+1 < len(tampered) && tampered[i+1] == '0' {
			tampered[i+1] = '9'
			break
		}
	}
	if err := os.WriteFile(path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load("in")
	te := clock.AsTraceError(err)
	if te == nil || te.Code != clock.CodeCheckpoint {
		t.Fatalf("tampered checkpoint must be rejected, got %v", err)
	}
}

func TestForeignInputRejected(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	st, _ := s.Load("input-A")
	if err := s.Advance(st, SegmentRecord{Index: 0}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load("input-B")
	te := clock.AsTraceError(err)
	if te == nil || te.Code != clock.CodeCheckpoint {
		t.Fatalf("foreign input checkpoint must be rejected, got %v", err)
	}
}

func TestCompletedDigestLookup(t *testing.T) {
	s, _ := OpenStore(t.TempDir())
	st, _ := s.Load("in")
	_ = s.Advance(st, SegmentRecord{Index: 0, Digest: "hash0"})
	if got := s.CompletedDigest(st, 0); got != "hash0" {
		t.Fatalf("digest = %q", got)
	}
	if got := s.CompletedDigest(st, 5); got != "" {
		t.Fatalf("future index must return empty, got %q", got)
	}
}
