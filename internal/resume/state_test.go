package resume_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/index"
	"github.com/zld/continuity/internal/resume"
)

var fp = index.Fingerprint{5, 6, 7}

func writeChunks(t *testing.T, dir string, fp index.Fingerprint, seqs ...uint64) {
	t.Helper()
	store, err := index.NewStore(dir, fp)
	if err != nil {
		t.Fatal(err)
	}
	for _, seq := range seqs {
		entries := []index.Entry{{
			Track: 1, Sample: uint32(seq),
			Time: clock.MustMake(int64(seq), 25), FileOffset: seq, Size: 8, Sync: true,
		}}
		if err := store.Write(index.Chunk{Seq: seq, Fingerprint: fp, Entries: entries}); err != nil {
			t.Fatalf("write %d: %v", seq, err)
		}
	}
}

func TestLoadFreshAndResumed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")

	fresh, err := resume.Load(dir, fp)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Resumed() || fresh.ResumedBlocks() != 0 || fresh.MaxSeq != -1 {
		t.Fatal("fresh directory looked resumed")
	}
	if fresh.ShouldSkip(0) {
		t.Fatal("nothing should be skippable on a fresh run")
	}

	writeChunks(t, dir, fp, 0, 1, 2)
	resumed, err := resume.Load(dir, fp)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Resumed() || resumed.ResumedBlocks() != 3 || resumed.MaxSeq != 2 {
		t.Fatalf("resume state = %+v", resumed)
	}
	if !resumed.ShouldSkip(1) || resumed.ShouldSkip(9) {
		t.Fatal("skip predicate wrong")
	}
	if len(resumed.CommittedEntries()) != 3 {
		t.Fatal("committed entries not surfaced")
	}
	if err := resumed.EnsureContiguous(); err != nil {
		t.Fatalf("contiguous prefix rejected: %v", err)
	}
}

func TestResumeRejectsChangedInput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	writeChunks(t, dir, fp, 0, 1)
	changed := fp
	changed[0] = 99
	_, err := resume.Load(dir, changed)
	if !errors.Is(err, clock.ErrResumeMismatch) {
		t.Fatalf("changed input: want ErrResumeMismatch, got %v", err)
	}
}

func TestResumeRejectsCorruptChunk(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	writeChunks(t, dir, fp, 0, 1)
	p := filepath.Join(dir, "chunk-000001.chk")
	raw, _ := os.ReadFile(p)
	raw[len(raw)-2] ^= 0x0F
	os.WriteFile(p, raw, 0o600)

	_, err := resume.Load(dir, fp)
	if !errors.Is(err, clock.ErrResumeMismatch) {
		t.Fatalf("corrupt chunk: want ErrResumeMismatch, got %v", err)
	}
}

func TestEnsureContiguousDetectsHole(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	// Write 0 and 2 directly, leaving 1 absent.
	writeChunks(t, dir, fp, 0)
	writeChunks(t, dir, fp, 2)
	st, err := resume.Load(dir, fp)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureContiguous(); !errors.Is(err, clock.ErrResumeMismatch) {
		t.Fatalf("hole: want mismatch, got %v", err)
	}
}

func TestRepeatedLoadSummaryStable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	writeChunks(t, dir, fp, 0, 1, 2)
	a, err := resume.Load(dir, fp)
	if err != nil {
		t.Fatal(err)
	}
	b, err := resume.Load(dir, fp)
	if err != nil {
		t.Fatal(err)
	}
	if a.ResumedBlocks() != b.ResumedBlocks() ||
		len(a.CommittedEntries()) != len(b.CommittedEntries()) ||
		a.MaxSeq != b.MaxSeq {
		t.Fatal("resume summary not stable across repeated loads")
	}
}
