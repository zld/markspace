package index_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/index"
)

var testFP = index.Fingerprint{1, 2, 3, 4}

func chunk(seq uint64, fp index.Fingerprint, n int) index.Chunk {
	entries := make([]index.Entry, n)
	for i := range entries {
		entries[i] = index.Entry{
			Track:      1,
			Sample:     uint32(seq)*100 + uint32(i),
			Time:       clock.MustMake(int64(seq)*100+int64(i), 25),
			FileOffset: uint64(1000 + seq*100 + uint64(i)),
			Size:       10,
			Sync:       i == 0,
		}
	}
	return index.Chunk{Seq: seq, Fingerprint: fp, Entries: entries}
}

func TestCheckpointRoundTripAndRecover(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	store, err := index.NewStore(dir, testFP)
	if err != nil {
		t.Fatal(err)
	}
	for seq := uint64(0); seq < 3; seq++ {
		if err := store.Write(chunk(seq, testFP, 4)); err != nil {
			t.Fatalf("write chunk %d: %v", seq, err)
		}
	}
	rec, err := store.Recover()
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(rec.Committed) != 3 || rec.Committed[0] != 0 || rec.Committed[2] != 2 {
		t.Fatalf("committed seqs = %v", rec.Committed)
	}
	if len(rec.Entries) != 12 {
		t.Fatalf("recovered entries = %d", len(rec.Entries))
	}
	// Rebuild an index from recovered entries and confirm the summary matches
	// a fresh in-memory build (deterministic repeat-run requirement).
	ixRecovered := index.New()
	ixRecovered.Add(rec.Entries)
	fresh := index.New()
	for seq := uint64(0); seq < 3; seq++ {
		fresh.Add(chunk(seq, testFP, 4).Entries)
	}
	if ixRecovered.EntryCount() != fresh.EntryCount() ||
		ixRecovered.SyncCount() != fresh.SyncCount() {
		t.Fatal("recovered index summary differs from fresh build")
	}
}

func TestUncommittedTmpIsNotRecovered(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	store, _ := index.NewStore(dir, testFP)
	if err := store.Write(chunk(0, testFP, 2)); err != nil {
		t.Fatal(err)
	}
	// A leftover temp file simulates a crash before atomic rename.
	if err := os.WriteFile(filepath.Join(dir, "chunk-000001.chk.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := store.Recover()
	if err != nil {
		t.Fatalf("recover should ignore temp files: %v", err)
	}
	if len(rec.Committed) != 1 || rec.Committed[0] != 0 {
		t.Fatalf("temp file treated as committed: %v", rec.Committed)
	}
}

func TestCorruptCheckpointRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	store, _ := index.NewStore(dir, testFP)
	if err := store.Write(chunk(1, testFP, 3)); err != nil {
		t.Fatal(err)
	}
	// Flip a payload byte inside the committed chunk.
	p := filepath.Join(dir, "chunk-000001.chk")
	raw, _ := os.ReadFile(p)
	raw[len(raw)-1] ^= 0xFF
	os.WriteFile(p, raw, 0o600)

	_, err := store.Recover()
	if !errors.Is(err, clock.ErrResumeMismatch) {
		t.Fatalf("corrupt chunk: want ErrResumeMismatch, got %v", err)
	}
}

func TestFingerprintChangeRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	store, _ := index.NewStore(dir, testFP)
	if err := store.Write(chunk(0, testFP, 2)); err != nil {
		t.Fatal(err)
	}
	changed := testFP
	changed[0] = 99
	other, err := index.NewStore(dir, changed)
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.Recover()
	if !errors.Is(err, clock.ErrResumeMismatch) {
		t.Fatalf("changed input: want ErrResumeMismatch, got %v", err)
	}
}

func TestWriteRejectsForeignFingerprint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	store, _ := index.NewStore(dir, testFP)
	foreign := testFP
	foreign[7] = 7
	err := store.Write(chunk(0, foreign, 1))
	if !errors.Is(err, clock.ErrResumeMismatch) {
		t.Fatalf("foreign fingerprint write: %v", err)
	}
}

func TestFingerprintFileStable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "input.mp4")
	if err := os.WriteFile(p, []byte("deterministic-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := index.FingerprintFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := index.FingerprintFile(p)
	if err != nil || a != b {
		t.Fatal("fingerprint not stable across reads")
	}
}
