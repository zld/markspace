package index

import (
	"os"
	"path/filepath"
	"testing"

	"avbatch/internal/mediaerr"
)

// entryAt builds a one-sample entry, so the block budget equals the entry
// count and block boundaries are easy to reason about.
func entryAt(i int) Entry {
	return Entry{
		Kind:        "segment",
		StartNanos:  int64(i) * 1_000_000_000,
		EndNanos:    int64(i+1) * 1_000_000_000,
		SampleCount: 1,
		Bytes:       int64(1000 + i),
		Source:      []int{0, 1},
	}
}

// entryWithSamples builds an entry carrying many media samples.
func entryWithSamples(i, samples int) Entry {
	e := entryAt(i)
	e.SampleCount = samples
	return e
}

func digest(b byte) [32]byte {
	var d [32]byte
	d[0] = b
	return d
}

func writeRun(t *testing.T, path string, n int, input [32]byte, blockSamples int) Header {
	t.Helper()
	w, err := NewWriter(Config{Path: path, BlockSamples: blockSamples, InputDigest: input})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := w.Add(entryAt(i)); err != nil {
			t.Fatal(err)
		}
	}
	hdr, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}
	return hdr
}

func TestWriteAndReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.bin")
	in := digest(7)
	hdr := writeRun(t, path, 10, in, 4)
	if hdr.Blocks != 3 {
		t.Fatalf("blocks = %d, want 3", hdr.Blocks)
	}
	if hdr.EntryCount != 10 {
		t.Fatalf("entries = %d", hdr.EntryCount)
	}
	r, err := Open(path, in)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(r.Entries) != 10 {
		t.Fatalf("recovered %d entries", len(r.Entries))
	}
	if r.Recovery.BlocksValid != 3 || r.Recovery.Corrupt {
		t.Fatalf("recovery = %+v", r.Recovery)
	}
	if n, err := r.ResumeFrom(in); err != nil || n != 10 {
		t.Fatalf("resume = %d, %v", n, err)
	}
	for i, e := range r.Entries {
		if e.Index != i && e.Index != entryAt(i).Index {
			// Writer renumbers; recovered order is what matters.
			t.Fatalf("entry %d index = %d", i, e.Index)
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary file survived commit")
	}
}

func TestAtomicReplacementLeavesOldFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.bin")
	in := digest(1)
	writeRun(t, path, 3, in, 2)
	before, _ := os.ReadFile(path)

	w, err := NewWriter(Config{Path: path, BlockSamples: 2, InputDigest: in})
	if err != nil {
		t.Fatal(err)
	}
	w.Add(entryAt(99))
	// Abandon without Commit: the published file must still be the old one.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("published index changed without a commit")
	}
}

func TestResumeRejectsDigestChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.bin")
	writeRun(t, path, 6, digest(1), 3)
	r, err := Open(path, digest(2))
	if !mediaerr.Is(err, mediaerr.KindResumeMismatch) {
		t.Fatalf("err = %v, want resume_mismatch", err)
	}
	if !r.Recovery.DigestMismatch {
		t.Fatal("digest mismatch not recorded")
	}
	if _, err := r.ResumeFrom(digest(2)); !mediaerr.Is(err, mediaerr.KindResumeMismatch) {
		t.Fatalf("resume err = %v", err)
	}
	// Verified blocks stay readable even when resume is refused.
	if len(r.Entries) != 6 {
		t.Fatalf("entries = %d", len(r.Entries))
	}
}

func TestTruncatedTailIsRecoverable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.bin")
	in := digest(3)
	writeRun(t, path, 12, in, 4)
	full, _ := os.ReadFile(path)

	// Cut in the middle of the third block.
	r, err := Open(path, in)
	if err != nil {
		t.Fatal(err)
	}
	// Locate the third block by parsing offsets from a known-good run.
	cut := len(full) - 10
	if err := os.WriteFile(path, full[:cut], 0o644); err != nil {
		t.Fatal(err)
	}
	r, err = Open(path, in)
	if !mediaerr.Is(err, mediaerr.KindTruncatedIndex) {
		t.Fatalf("err = %v, want truncated_index", err)
	}
	if r.Recovery.BlocksValid != 2 {
		t.Fatalf("valid blocks = %d, want 2", r.Recovery.BlocksValid)
	}
	if len(r.Entries) != 8 {
		t.Fatalf("recovered entries = %d, want 8", len(r.Entries))
	}
	if r.Recovery.ResumeOffset <= 0 {
		t.Fatalf("resume offset = %d", r.Recovery.ResumeOffset)
	}
	if _, err := r.ResumeFrom(in); !mediaerr.Is(err, mediaerr.KindTruncatedIndex) {
		t.Fatalf("resume err = %v", err)
	}
	_ = r
}

func TestCorruptBlockPayloadDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.bin")
	in := digest(4)
	writeRun(t, path, 8, in, 4)
	data, _ := os.ReadFile(path)
	// Flip a byte inside the second block's payload.
	pos := len(data) - 20
	data[pos] ^= 0xFF
	os.WriteFile(path, data, 0o644)
	r, err := Open(path, in)
	if err == nil {
		t.Fatal("corruption was not detected")
	}
	if r.Recovery.BlocksValid < 1 {
		t.Fatalf("verified prefix lost: %+v", r.Recovery)
	}
	if len(r.Entries) != 4 {
		t.Fatalf("entries = %d, want the first block only", len(r.Entries))
	}
}

func TestBadMagicDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.bin")
	in := digest(5)
	writeRun(t, path, 2, in, 2)
	data, _ := os.ReadFile(path)
	copy(data, "NOPE")
	os.WriteFile(path, data, 0o644)
	_, err := Open(path, in)
	if !mediaerr.Is(err, mediaerr.KindCorruptCheckpoint) {
		t.Fatalf("err = %v, want corrupt_checkpoint", err)
	}
}

func TestShortFileDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.bin")
	os.WriteFile(path, []byte("AVBI"), 0o644)
	_, err := Open(path, digest(1))
	if !mediaerr.Is(err, mediaerr.KindCorruptCheckpoint) {
		t.Fatalf("err = %v, want corrupt_checkpoint", err)
	}
}

func TestDuplicateWritesAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.bin")
	in := digest(6)
	writeRun(t, path, 5, in, 2)
	first, _ := os.ReadFile(path)
	// Rewriting the identical input must produce identical bytes.
	writeRun(t, path, 5, in, 2)
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("identical input produced different checkpoint bytes")
	}
	r, err := Open(path, in)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := ManifestDigest(r.Entries)
	r2, _ := Open(path, in)
	b, _ := ManifestDigest(r2.Entries)
	if a != b {
		t.Fatal("manifest digest is not stable")
	}
}

func TestEmptyIndexCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.bin")
	w, err := NewWriter(Config{Path: path, BlockSamples: 4, InputDigest: digest(1)})
	if err != nil {
		t.Fatal(err)
	}
	hdr, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Blocks != 0 || hdr.EntryCount != 0 {
		t.Fatalf("header = %+v", hdr)
	}
	r, err := Open(path, digest(1))
	if err != nil {
		t.Fatalf("open empty: %v", err)
	}
	if len(r.Entries) != 0 {
		t.Fatalf("entries = %d", len(r.Entries))
	}
	if n, err := r.ResumeFrom(digest(1)); err != nil || n != 0 {
		t.Fatalf("resume = %d, %v", n, err)
	}
}

func TestBlockSizeValidation(t *testing.T) {
	if _, err := NewWriter(Config{BlockSamples: 0}); err == nil {
		t.Fatal("zero block size accepted")
	}
	if _, err := NewWriter(Config{BlockSamples: 10, MaxBlockSamples: 5}); err == nil {
		t.Fatal("oversized block accepted")
	}
}

func TestBlockBudgetUsesMediaSamples(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.bin")
	w, err := NewWriter(Config{Path: path, BlockSamples: 10, InputDigest: digest(1)})
	if err != nil {
		t.Fatal(err)
	}
	// Two six-sample entries: the second crosses the budget, so the block
	// closes after it rather than splitting it.
	w.Add(entryWithSamples(0, 6))
	w.Add(entryWithSamples(1, 6))
	if _, err := w.Add(entryWithSamples(2, 1)); err != nil {
		t.Fatal(err)
	}
	hdr, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Blocks != 2 {
		t.Fatalf("blocks = %d, want 2", hdr.Blocks)
	}
	r, err := Open(path, digest(1))
	if err != nil {
		t.Fatal(err)
	}
	if r.Recovery.BlocksValid != 2 || len(r.Entries) != 3 {
		t.Fatalf("recovery = %+v entries=%d", r.Recovery, len(r.Entries))
	}
}

func TestMemoryOnlyWriter(t *testing.T) {
	w, err := NewWriter(Config{BlockSamples: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		w.Add(entryAt(i))
	}
	hdr, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Blocks != 3 || hdr.EntryCount != 5 {
		t.Fatalf("header = %+v", hdr)
	}
}
