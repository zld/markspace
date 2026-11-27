package batch

import (
	"os"
	"path/filepath"
	"testing"

	"tracecheck/internal/clock"
	"tracecheck/internal/ebml"
	"tracecheck/internal/index"
	"tracecheck/internal/mkv"
	"tracecheck/internal/serr"
)

func tb() clock.Timebase {
	return clock.Timebase{TickNanos: clock.NanosPerMilli, ClientUnitNanos: clock.NanosPerMilli}
}

// fixture builds an unknown-length Segment with two unknown-length Clusters
// and four SimpleBlocks so that several chunks exist at chunkSize 2.
func fixture(t *testing.T) []byte {
	t.Helper()
	block := func(track uint64, rel int16, key bool, frame byte) []byte {
		tv := ebml.MustEncodeVINT(track)
		flags := byte(0x00)
		if key {
			flags = 0x80
		}
		return append(append([]byte{}, tv...), byte(uint16(rel)>>8), byte(uint16(rel)), flags, frame)
	}
	mkCluster := func(ts uint64, blocks ...[]byte) []byte {
		var c ebml.Builder
		_ = c.UintElement(ebml.IDTimestamp, ts)
		for _, b := range blocks {
			_ = c.Element(ebml.IDSimpleBlock, b)
		}
		return c.Bytes()
	}
	var info, tracks ebml.Builder
	_ = info.UintElement(ebml.IDTimecodeScale, 1_000_000)
	_ = tracks.Container(ebml.IDTrackEntry, func(e *ebml.Builder) error {
		return e.UintElement(ebml.IDTrackNumber, 1)
	})
	var seg ebml.Builder
	_ = seg.Element(ebml.IDInfo, info.Bytes())
	_ = seg.Element(ebml.IDTracks, tracks.Bytes())
	_ = seg.UnknownElement(ebml.IDCluster, mkCluster(0,
		block(1, 0, true, 0xA0), block(1, 20, false, 0xA1)))
	_ = seg.UnknownElement(ebml.IDCluster, mkCluster(40,
		block(1, 0, true, 0xA2), block(1, 20, false, 0xA3)))
	var top ebml.Builder
	_ = top.Element(ebml.IDSegment, seg.Bytes())
	return top.Bytes()
}

func TestRunDeterministic(t *testing.T) {
	data := fixture(t)
	a, err := Run(data, tb(), "1ms", Options{ChunkSize: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(data, tb(), "1ms", Options{ChunkSize: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.IndexDigest != b.IndexDigest {
		t.Fatalf("index digest differs: %s vs %s", a.IndexDigest, b.IndexDigest)
	}
	if a.TotalChunks != 2 || a.CompletedChunks != 2 || a.Reprocessed != 2 {
		t.Fatalf("chunk accounting: %+v", a)
	}
	if len(a.SegmentHashes) != 2 || a.SegmentHashes[0] != b.SegmentHashes[0] {
		t.Fatalf("segment hashes unstable: %+v vs %+v", a.SegmentHashes, b.SegmentHashes)
	}
}

func TestResumeReprocessesZero(t *testing.T) {
	data := fixture(t)
	first, err := Run(data, tb(), "1ms", Options{ChunkSize: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	prior := first.Checkpoint("input.mkv", first.MetadataDigest)
	second, err := Run(data, tb(), "1ms", Options{ChunkSize: 2, Resume: true}, prior)
	if err != nil {
		t.Fatal(err)
	}
	if second.Reprocessed != 0 {
		t.Fatalf("resume reprocessed %d chunks", second.Reprocessed)
	}
	if second.IndexDigest != first.IndexDigest {
		t.Fatal("resume changed the index digest")
	}
	if len(second.SegmentHashes) != len(first.SegmentHashes) {
		t.Fatalf("resume changed hashes: %d vs %d", len(second.SegmentHashes), len(first.SegmentHashes))
	}
	for i := range first.SegmentHashes {
		if first.SegmentHashes[i] != second.SegmentHashes[i] {
			t.Fatalf("segment hash %d changed", i)
		}
	}
}

func TestResumeMismatchedInput(t *testing.T) {
	data := fixture(t)
	first, _ := Run(data, tb(), "1ms", Options{ChunkSize: 2}, nil)
	prior := first.Checkpoint("input.mkv", first.MetadataDigest)
	prior.InputSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	_, err := Run(data, tb(), "1ms", Options{ChunkSize: 2, Resume: true}, prior)
	if serr.CodeOf(err) != serr.CodeCheckpointCorrupt {
		t.Fatalf("mismatched digest not rejected: %v", err)
	}
}

func TestRunChunkSizeValidation(t *testing.T) {
	if _, err := Run(fixture(t), tb(), "1ms", Options{ChunkSize: 0}, nil); serr.CodeOf(err) != serr.CodeInvalidArgs {
		t.Fatalf("zero chunk size: %v", err)
	}
}

func TestCommitAndResume(t *testing.T) {
	dir := t.TempDir()
	data := fixture(t)
	sum, err := Run(data, tb(), "1ms", Options{ChunkSize: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Commit(dir, "input.mkv", "/tmp/input.mkv", sum); err != nil {
		t.Fatal(err)
	}
	_, prior, err := Resume(data, dir, "input.mkv", Options{ChunkSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if prior == nil {
		t.Fatal("checkpoint not found after commit")
	}
	resumed, err := Run(data, tb(), "1ms", Options{ChunkSize: 2, Resume: true}, prior)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Reprocessed != 0 {
		t.Fatalf("commit/resume reprocessed=%d", resumed.Reprocessed)
	}
}

func TestCorruptCheckpointOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := index.CheckpointPath(dir, "input.mkv")
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Resume(nil, dir, "input.mkv", Options{ChunkSize: 2})
	if serr.CodeOf(err) != serr.CodeCheckpointCorrupt {
		t.Fatalf("want CHECKPOINT_CORRUPT, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "input.mkv")); statErr == nil {
		t.Fatal("unexpected file")
	}
}

func TestResumeDetectsHashMismatch(t *testing.T) {
	data := fixture(t)
	first, _ := Run(data, tb(), "1ms", Options{ChunkSize: 2}, nil)
	prior := first.Checkpoint("input.mkv", first.MetadataDigest)
	prior.SegmentHashes[1] = "tampered"
	_, err := Run(data, tb(), "1ms", Options{ChunkSize: 2, Resume: true}, prior)
	if serr.CodeOf(err) != serr.CodeCheckpointCorrupt {
		t.Fatalf("tampered hash not rejected: %v", err)
	}
}

func TestResumeDetectsOffsetMismatch(t *testing.T) {
	data := fixture(t)
	first, _ := Run(data, tb(), "1ms", Options{ChunkSize: 2}, nil)
	prior := first.Checkpoint("input.mkv", first.MetadataDigest)
	prior.CompletedOffset = prior.CompletedOffset - 1
	_, err := Run(data, tb(), "1ms", Options{ChunkSize: 2, Resume: true}, prior)
	if serr.CodeOf(err) != serr.CodeCheckpointCorrupt {
		t.Fatalf("wrong completed offset not rejected: %v", err)
	}
}

func TestResumePartialPrefix(t *testing.T) {
	data := fixture(t)
	first, _ := Run(data, tb(), "1ms", Options{ChunkSize: 1}, nil)
	// Simulate a crash after the first of four chunks: keep one chunk and its
	// boundary, then resume; only the remaining three are reprocessed.
	partial := first.Checkpoint("input.mkv", first.MetadataDigest)
	partial.CompletedChunks = 1
	partial.SegmentHashes = partial.SegmentHashes[:1]
	// The boundary must match where the first chunk ends and no further.
	partial.CompletedOffset = chunkBoundary(t, data, 1)
	second, err := Run(data, tb(), "1ms", Options{ChunkSize: 1, Resume: true}, partial)
	if err != nil {
		t.Fatalf("partial resume: %v", err)
	}
	if second.Reprocessed != second.TotalChunks-1 {
		t.Fatalf("reprocessed=%d total=%d", second.Reprocessed, second.TotalChunks)
	}
	if second.IndexDigest != first.IndexDigest {
		t.Fatal("partial resume changed digest")
	}
}

// chunkBoundary recomputes the byte end of the first n single-record chunks by
// running the same parse/index pipeline the batch uses.
func chunkBoundary(t *testing.T, data []byte, n int) int64 {
	t.Helper()
	doc, err := ebml.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := mkv.ParseBlocks(doc)
	if err != nil {
		t.Fatal(err)
	}
	tracks, _ := mkv.ParseTracks(doc)
	idx, _ := index.Build(blocks, tb(), tracks.TimestampScale, "1ms")
	chunks := splitChunks(idx, data, 1)
	return endOffset(chunks, n)
}
