package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediapipe/internal/cache"
)

func testRecord(gen uint64, stage string) Record {
	return Record{
		Format: recordFormat, Generation: gen, Stage: stage, Version: Version,
		Input:     cache.KeyFromBytes([]byte("in")),
		Output:    cache.KeyFromBytes([]byte(stage)),
		SeqWindow: SeqWindow{Start: 10, End: 20, Count: 11},
		Committed: []CommittedFragment{{Sequence: uint32(gen), Digest: cache.KeyFromBytes([]byte(stage)), Tracks: []uint32{1, 2}}},
	}
}

func TestJournalAppendLoad(t *testing.T) {
	j, err := OpenJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if rec, n, err := j.Load(); rec != nil || n != 0 || err != nil {
		t.Fatalf("empty journal: %v %d %v", rec, n, err)
	}
	for gen := uint64(1); gen <= 3; gen++ {
		if err := j.Append(testRecord(gen, "index")); err != nil {
			t.Fatal(err)
		}
	}
	last, n, err := j.Load()
	if err != nil {
		t.Fatal(err)
	}
	if last == nil || last.Generation != 3 || n != 3 {
		t.Fatalf("last %+v n=%d", last, n)
	}
	all, err := j.Records()
	if err != nil || len(all) != 3 {
		t.Fatalf("records %d %v", len(all), err)
	}
}

func TestJournalIgnoresTornTail(t *testing.T) {
	dir := t.TempDir()
	j, _ := OpenJournal(dir)
	j.Append(testRecord(1, "index"))
	j.Append(testRecord(2, "index"))
	// Simulate a process death in the middle of writing record 3.
	f, err := os.OpenFile(filepath.Join(dir, "checkpoint.log"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("512 deadbeef {\"format\":\"mediapipe-check")
	f.Close()
	last, n, err := j.Load()
	if err != nil {
		t.Fatal(err)
	}
	if last == nil || last.Generation != 2 || n != 2 {
		t.Fatalf("torn tail broke recovery: %+v n=%d", last, n)
	}
}

func TestJournalRejectsDigestTampering(t *testing.T) {
	dir := t.TempDir()
	j, _ := OpenJournal(dir)
	rec := testRecord(1, "index")
	rec.StateDigest = "deadbeef"
	if err := j.Append(rec); !errors.Is(err, ErrCheckpointDigest) {
		t.Fatalf("tampered record appended: %v", err)
	}
	// A hand-edited record with a valid line checksum but stale state digest is
	// skipped by Load.
	good := testRecord(2, "index")
	good.StateDigest = good.computeDigest()
	j.Append(good)
	raw, err := os.ReadFile(filepath.Join(dir, "checkpoint.log"))
	if err != nil {
		t.Fatal(err)
	}
	_ = raw
	last, _, err := j.Load()
	if err != nil || last == nil || last.Generation != 2 {
		t.Fatalf("valid record lost: %v", err)
	}
}

func TestJournalRejectsGenerationRegression(t *testing.T) {
	j, _ := OpenJournal(t.TempDir())
	j.Append(testRecord(2, "index"))
	j.Append(testRecord(1, "index"))
	if _, _, err := j.Load(); !errors.Is(err, ErrCheckpointRegression) {
		t.Fatalf("regression accepted: %v", err)
	}
}

func TestJournalResetAndTruncate(t *testing.T) {
	dir := t.TempDir()
	j, _ := OpenJournal(dir)
	for gen := uint64(1); gen <= 4; gen++ {
		j.Append(testRecord(gen, "index"))
	}
	if err := j.Truncate(2); err != nil {
		t.Fatal(err)
	}
	all, _ := j.Records()
	if len(all) != 2 {
		t.Fatalf("truncate kept %d", len(all))
	}
	if err := j.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, n, _ := j.Load(); n != 0 {
		t.Fatalf("reset left %d records", n)
	}
}

func TestRecordValidation(t *testing.T) {
	rec := testRecord(1, "index")
	if err := rec.Validate(); err == nil {
		t.Fatal("record with missing state digest accepted")
	}
	rec.StateDigest = rec.computeDigest()
	if err := rec.Validate(); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	bad := rec
	bad.Format = "other"
	if err := bad.Validate(); !errors.Is(err, ErrCheckpointFormat) {
		t.Fatalf("bad format accepted")
	}
	outOfOrder := rec
	outOfOrder.Committed = []CommittedFragment{
		{Sequence: 2, Digest: cache.KeyFromBytes([]byte("a")), Tracks: []uint32{1}},
		{Sequence: 1, Digest: cache.KeyFromBytes([]byte("b")), Tracks: []uint32{1}},
	}
	outOfOrder.StateDigest = outOfOrder.computeDigest()
	if err := outOfOrder.Validate(); !errors.Is(err, ErrCheckpointFormat) {
		t.Fatalf("out-of-order commits accepted")
	}
}

func TestJournalBounds(t *testing.T) {
	j, _ := OpenJournal(t.TempDir())
	huge := testRecord(1, "index")
	huge.Committed = make([]CommittedFragment, MaxCommitted+1)
	for i := range huge.Committed {
		huge.Committed[i] = CommittedFragment{Sequence: uint32(i + 1), Digest: cache.KeyFromBytes([]byte{byte(i)}), Tracks: []uint32{1}}
	}
	huge.StateDigest = huge.computeDigest()
	if err := j.Append(huge); !errors.Is(err, ErrCheckpointFormat) {
		t.Fatalf("oversized record accepted: %v", err)
	}
}
