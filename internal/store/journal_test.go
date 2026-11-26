package store

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/fingerprint"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

func TestPutBytesIsContentAddressedAndIdempotent(t *testing.T) {
	s := newStore(t)
	payload := []byte("immutable block payload")
	digest := fingerprint.BlobDigest(payload)
	if err := s.PutBytes(digest, payload); err != nil {
		t.Fatalf("put: %v", err)
	}
	st1, err := os.Stat(mustPath(t, s, digest))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutBytes(digest, payload); err != nil {
		t.Fatalf("second put must be a no-op: %v", err)
	}
	st2, err := os.Stat(mustPath(t, s, digest))
	if err != nil {
		t.Fatal(err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) || st1.Size() != st2.Size() {
		t.Fatal("existing object was rewritten")
	}
	got, err := s.ReadObject(digest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("stored bytes differ")
	}
}

func TestPutBytesRejectsDigestMismatch(t *testing.T) {
	s := newStore(t)
	err := s.PutBytes(strings.Repeat("0", 64), []byte("x"))
	if fault.CodeOf(err) != fault.CodeDigest {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}

func TestReadObjectDetectsTampering(t *testing.T) {
	s := newStore(t)
	payload := []byte("verify me")
	digest := fingerprint.BlobDigest(payload)
	if err := s.PutBytes(digest, payload); err != nil {
		t.Fatal(err)
	}
	path := mustPath(t, s, digest)
	if err := os.WriteFile(path, []byte("Verify me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadObject(digest); fault.CodeOf(err) != fault.CodeDigest {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
	if err := s.VerifyObject(digest, int64(len(payload))); fault.CodeOf(err) != fault.CodeDigest {
		t.Fatalf("verify code = %q (%v)", fault.CodeOf(err), err)
	}
}

func TestVerifyObjectChecksSize(t *testing.T) {
	s := newStore(t)
	payload := []byte("sized")
	digest := fingerprint.BlobDigest(payload)
	if err := s.PutBytes(digest, payload); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyObject(digest, int64(len(payload))); err != nil {
		t.Fatalf("correct size: %v", err)
	}
	if err := s.VerifyObject(digest, int64(len(payload))+1); fault.CodeOf(err) != fault.CodeDigest {
		t.Fatalf("size mismatch not detected: %v", err)
	}
}

func TestInvalidDigestIsRejected(t *testing.T) {
	s := newStore(t)
	for _, d := range []string{"", "abc", strings.Repeat("z", 64)} {
		if _, err := s.Path(d); fault.CodeOf(err) != fault.CodeDigest {
			t.Fatalf("%q: code = %q", d, fault.CodeOf(err))
		}
	}
	if s.Exists("not-a-digest") {
		t.Fatal("invalid digest must not exist")
	}
}

func TestJournalAppendsAndReplays(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	recs := []Record{
		{Kind: "stage", AssetKey: "k", Stage: "SCANNED"},
		{Kind: "checkpoint", AssetKey: "k", Block: 0, Length: 1024, ContentHash: strings.Repeat("a", 64)},
		{Kind: "checkpoint", AssetKey: "k", Block: 1, Length: 1024, ContentHash: strings.Repeat("b", 64)},
	}
	for _, r := range recs {
		if err := j.Append(r); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got, err := j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].Block != 1 {
		t.Fatalf("replay = %+v", got)
	}
	rec := NewRecorder(got)
	if rec.CommittedBlocks()["k"] != 2 {
		t.Fatalf("committed = %d", rec.CommittedBlocks()["k"])
	}
	if last, ok := rec.LastCheckpoint("k"); !ok || last.Block != 1 {
		t.Fatalf("last checkpoint = %+v %v", last, ok)
	}
}

func TestJournalDiscardsTornTail(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := j.Append(Record{Kind: "checkpoint", AssetKey: "k", Block: i}); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(j.Path(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"checkpoint","asset_key":"k","block":3`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	recs, err := j.Replay()
	if err != nil {
		t.Fatalf("torn tail must not fail replay: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("replayed %d records, want the 3 committed ones", len(recs))
	}
}

func TestJournalRejectsCorruptMiddleLine(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.Path(), []byte("{not json}\n{\"kind\":\"stage\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Replay(); fault.CodeOf(err) != fault.CodeCheckpoint {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}

func TestReplayEmptyJournal(t *testing.T) {
	j, err := OpenJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recs, err := j.Replay()
	if err != nil || len(recs) != 0 {
		t.Fatalf("empty journal: %v %v", recs, err)
	}
}

func TestWriteFileAtomicLeavesNoTemp(t *testing.T) {
	s := newStore(t)
	if err := s.WriteFileAtomic("meta/a.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	b, err := s.ReadFile("meta/a.json")
	if err != nil || string(b) != "{}" {
		t.Fatalf("read back: %q %v", b, err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Root(), "meta"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func mustPath(t *testing.T, s *Store, digest string) string {
	t.Helper()
	p, err := s.Path(digest)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPutBytesVerifiesExistingObject(t *testing.T) {
	s := newStore(t)
	payload := []byte("same-size tamper")
	digest := fingerprint.BlobDigest(payload)
	if err := s.PutBytes(digest, payload); err != nil {
		t.Fatal(err)
	}
	// Replace with a same-length but different payload: a size-only check would
	// accept it, so the store must re-hash before treating the write as a no-op.
	tampered := append([]byte(nil), payload...)
	tampered[0] ^= 0xFF
	if err := os.WriteFile(mustPath(t, s, digest), tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.PutBytes(digest, payload); fault.CodeOf(err) != fault.CodeDigest {
		t.Fatalf("same-size tamper not detected: %v", err)
	}
	// Repair restores the verified bytes.
	if err := s.RepairBytes(digest, payload); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if err := s.VerifyObject(digest, int64(len(payload))); err != nil {
		t.Fatalf("repaired object: %v", err)
	}
}

func TestRepairBytesRejectsWrongDigest(t *testing.T) {
	s := newStore(t)
	if err := s.RepairBytes(strings.Repeat("0", 64), []byte("x")); fault.CodeOf(err) != fault.CodeDigest {
		t.Fatalf("code = %q", fault.CodeOf(err))
	}
}
