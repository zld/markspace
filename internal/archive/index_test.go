package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func sampleRow(key string, order uint64) ManifestRow {
	return ManifestRow{
		StageKey:    key,
		Track:       "demo/t0(video)",
		Order:       order,
		FragmentSHA: "frag-" + key,
		FrameSHA:    "frame-" + key,
		SegmentSHA:  "seg-" + key,
		DTSNs:       int64(order) * 33_366_666,
	}
}

func TestChunkedWriteAndResume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	idx, err := OpenIndex(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	for k := 0; k < 7; k++ {
		if !idx.AddRow(sampleRow(string(rune('a'+k)), uint64(k))) {
			t.Fatalf("row %d should be accepted", k)
		}
	}
	chunks, err := idx.FlushAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 { // 3 + 3 + 1
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	if idx.Len() != 7 {
		t.Fatalf("rows = %d, want 7", idx.Len())
	}

	// Reopen: checkpoint + chunks must rebuild identical state.
	reopened, err := OpenIndex(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Len() != 7 {
		t.Fatalf("resumed rows = %d, want 7", reopened.Len())
	}
	if len(reopened.Chunks()) != 3 {
		t.Fatalf("resumed chunks = %d, want 3", len(reopened.Chunks()))
	}
	// Re-delivering the same keys adds nothing.
	for k := 0; k < 7; k++ {
		if reopened.AddRow(sampleRow(string(rune('a'+k)), uint64(k))) {
			t.Fatalf("duplicate delivery of row %d must be rejected", k)
		}
	}
	if reopened.Len() != 7 {
		t.Fatal("re-delivery must not grow the manifest")
	}
}

func TestInterruptedChunkNotCommitted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	idx, err := OpenIndex(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	idx.AddRow(sampleRow("a", 0))
	idx.AddRow(sampleRow("b", 1))
	if _, err := idx.FlushChunk(); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash while writing the next chunk: leave a temp file and
	// a chunk path without any checkpoint entry.
	archDir := filepath.Join(dir, "archive")
	stray := filepath.Join(archDir, "chunk-0001.json")
	if err := os.WriteFile(stray, []byte("{incomplete"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The checkpoint must still reference only chunk 0.
	cp, err := readCheckpoint(archDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Chunks) != 1 {
		t.Fatalf("committed chunks = %d, want 1", len(cp.Chunks))
	}

	// Reopen must ignore the uncommitted stray and keep one clean chunk.
	reopened, err := OpenIndex(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Chunks()) != 1 || reopened.Len() != 2 {
		t.Fatalf("resume must not adopt uncommitted chunk: chunks=%d rows=%d",
			len(reopened.Chunks()), reopened.Len())
	}
}

func TestResumeMatchesCleanRun(t *testing.T) {
	dir1 := filepath.Join(t.TempDir(), "clean")
	clean, err := OpenIndex(dir1, 2)
	if err != nil {
		t.Fatal(err)
	}
	for k := 0; k < 6; k++ {
		clean.AddRow(sampleRow(string(rune('a'+k)), uint64(k)))
	}
	if _, err := clean.FlushAll(); err != nil {
		t.Fatal(err)
	}

	// Interrupted run: flush one chunk, then reopen and finish the rest.
	dir2 := filepath.Join(t.TempDir(), "interrupted")
	part, err := OpenIndex(dir2, 2)
	if err != nil {
		t.Fatal(err)
	}
	for k := 0; k < 2; k++ {
		part.AddRow(sampleRow(string(rune('a'+k)), uint64(k)))
	}
	if _, err := part.FlushChunk(); err != nil {
		t.Fatal(err)
	}
	resumed, err := OpenIndex(dir2, 2)
	if err != nil {
		t.Fatal(err)
	}
	for k := 2; k < 6; k++ {
		resumed.AddRow(sampleRow(string(rune('a'+k)), uint64(k)))
	}
	if _, err := resumed.FlushAll(); err != nil {
		t.Fatal(err)
	}

	cleanRows := clean.Rows()
	resumedRows := resumed.Rows()
	if len(cleanRows) != len(resumedRows) {
		t.Fatalf("row count clean=%d resumed=%d", len(cleanRows), len(resumedRows))
	}
	cleanChunks := clean.Chunks()
	resumedChunks := resumed.Chunks()
	if len(cleanChunks) != len(resumedChunks) {
		t.Fatalf("chunk count clean=%d resumed=%d", len(cleanChunks), len(resumedChunks))
	}
	for i := range cleanChunks {
		if cleanChunks[i].SHA256 != resumedChunks[i].SHA256 {
			t.Fatalf("chunk %d hash differs after resume", i)
		}
	}
}
