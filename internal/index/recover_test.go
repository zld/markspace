package index_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/index"
)

func tempStore(t *testing.T) (*index.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := index.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func key() string { return index.ResumeKey("inputsha", "configsha", "segments") }

func TestWriteAndRecoverLastComplete(t *testing.T) {
	s, _ := tempStore(t)
	k := key()
	for seq, cursor := range []uint64{10, 20, 30, 40} {
		if _, err := s.Write(index.Record{
			CheckpointKey: k, ChunkSeq: uint64(seq + 1), Cursor: cursor, Stage: "segments",
		}); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := index.Recover(s, k)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.FoundAny || rep.FromCursor != 40 || rep.Replayed != 4 {
		t.Fatalf("rep=%+v want cursor 40, 4 chunks", rep)
	}
	if rep.RolledBack != 0 {
		t.Fatalf("unexpected rollback count %d", rep.RolledBack)
	}
}

func TestHalfWrittenTempRolledBack(t *testing.T) {
	s, dir := tempStore(t)
	k := key()
	if _, err := s.Write(index.Record{CheckpointKey: k, ChunkSeq: 1, Cursor: 10, Stage: "segments"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-write: an orphaned .tmp file.
	tmp := filepath.Join(dir, "chunk-000000000002.chk.tmp")
	if err := os.WriteFile(tmp, []byte("MCK1partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := index.Recover(s, k)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RolledBack != 1 || rep.FromCursor != 10 {
		t.Fatalf("rep=%+v want 1 rollback, cursor 10", rep)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("temp file must be removed after rollback")
	}
}

func TestCorruptChecksumRollback(t *testing.T) {
	s, dir := tempStore(t)
	k := key()
	if _, err := s.Write(index.Record{CheckpointKey: k, ChunkSeq: 1, Cursor: 10, Stage: "segments"}); err != nil {
		t.Fatal(err)
	}
	corruptPath := filepath.Join(dir, "chunk-000000000002.chk")
	// Valid frame header but flipped payload byte, breaking the checksum.
	good := filepath.Join(dir, "chunk-000000000001.chk")
	frame, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	frame[len(frame)-1] ^= 0xFF
	if err := os.WriteFile(corruptPath, frame, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := index.Recover(s, k)
	if err != nil {
		t.Fatalf("corruption must be rolled back, not fatal: %v", err)
	}
	if rep.RolledBack != 1 || rep.FromCursor != 10 {
		t.Fatalf("rep=%+v want rollback to cursor 10", rep)
	}
	if _, err := os.Stat(corruptPath); !os.IsNotExist(err) {
		t.Fatal("corrupt checkpoint must be removed")
	}
}

func TestResumeDoesNotDoubleCountCommittedChunks(t *testing.T) {
	s, _ := tempStore(t)
	k := key()
	// First run processed 40 blocks across 4 chunks.
	for seq, cursor := range []uint64{10, 20, 30, 40} {
		if _, err := s.Write(index.Record{CheckpointKey: k, ChunkSeq: uint64(seq + 1), Cursor: cursor, Stage: "segments"}); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := index.Recover(s, k)
	if err != nil {
		t.Fatal(err)
	}
	// Resume continues at the recovered cursor, writing chunk seq 5 onward.
	if _, err := s.Write(index.Record{
		CheckpointKey: k, ChunkSeq: 5, Cursor: 50, Stage: "segments",
	}); err != nil {
		t.Fatal(err)
	}
	all, err := s.List(k)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("records=%d want 5 (no duplicates of 1..4)", len(all))
	}
	cursors := map[uint64]bool{}
	for _, r := range all {
		if cursors[r.Cursor] {
			t.Fatalf("duplicate cursor %d after resume", r.Cursor)
		}
		cursors[r.Cursor] = true
	}
	if rep.Replayed != 4 {
		t.Fatalf("replayed=%d want 4", rep.Replayed)
	}
}

func TestForeignKeyRejected(t *testing.T) {
	s, _ := tempStore(t)
	if _, err := s.Write(index.Record{
		CheckpointKey: index.ResumeKey("other", "x", "segments"), ChunkSeq: 1, Cursor: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Recover(s, key()); err == nil {
		t.Fatal("foreign checkpoint key must fail closed")
	}
}

func TestDeterministicRecordFraming(t *testing.T) {
	s, _ := tempStore(t)
	rec := index.Record{CheckpointKey: key(), ChunkSeq: 7, Cursor: 70, Stage: "segments"}
	st1, err := s.Write(rec)
	if err != nil {
		t.Fatal(err)
	}
	frame1, _ := os.ReadFile(st1.Path)
	os.Remove(st1.Path)
	st2, err := s.Write(rec)
	if err != nil {
		t.Fatal(err)
	}
	frame2, _ := os.ReadFile(st2.Path)
	if string(frame1) != string(frame2) {
		t.Fatal("identical records must produce identical frames")
	}
	if len(st2.SHA256) != 64 {
		t.Fatal("stored checksum must be 64 hex chars")
	}
}
