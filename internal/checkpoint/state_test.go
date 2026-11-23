package checkpoint

import (
	"fmt"
	"os"
	"testing"

	"mediqc/internal/fault"
)

func TestCommitThenLoad(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Next != 0 {
		t.Fatalf("fresh state next=%d", st.Next)
	}
	for i := 0; i < 3; i++ {
		d := fmt.Sprintf("digest-%d", i)
		if err := s.Commit(st, d, []byte("record-"+d)); err != nil {
			t.Fatal(err)
		}
	}
	if st.Next != 3 {
		t.Fatalf("next=%d", st.Next)
	}
	// Reload from disk: progress survives a restart.
	st2, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st2.Next != 3 || len(st2.Committed) != 3 || st2.Committed[2] != "digest-2" {
		t.Fatalf("reloaded state: %+v", st2)
	}
}

func TestSegmentRecordWrittenBeforeCheckpoint(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Load()
	if err := s.Commit(st, "abc", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.SegmentPath("abc"))
	if err != nil || string(raw) != "payload" {
		t.Fatalf("segment record: %q %v", raw, err)
	}
}

func TestStaleCheckpointRejected(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Load()
	if err := s.Commit(st, "gone", []byte("x")); err != nil {
		t.Fatal(err)
	}
	// Lose the segment record: checkpoint now points at nothing.
	if err := os.Remove(s.SegmentPath("gone")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); !fault.IsCode(err, fault.CodeCheckpointStale) {
		t.Fatalf("want E_CHECKPOINT_STALE, got %v", err)
	}
}

func TestCommittedSegmentsNotRecommitted(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Load()
	for i := 0; i < 5; i++ {
		if err := s.Commit(st, fmt.Sprintf("d%d", i), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate resume: skip indices below Next.
	st2, _ := s.Load()
	processed := 0
	for i := 0; i < 5; i++ {
		if i < st2.Next {
			continue // already committed
		}
		processed++
	}
	if processed != 0 {
		t.Fatalf("resume reprocessed %d committed segments", processed)
	}
}
