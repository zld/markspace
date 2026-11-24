package pipeline

import (
	"path/filepath"
	"testing"

	"mediarc/internal/ingest"
	"mediarc/internal/model"
	"mediarc/internal/recovery"
)

func TestTruncatedRecordOnChunkBoundaryDoesNotShiftChunks(t *testing.T) {
	dir := t.TempDir()
	framesPath := filepath.Join(dir, "frames.mrc1")
	const total = 24
	const chunk = 8
	const boundaryIndex = chunk*2 - 1 // last frame of the second chunk
	fx, err := ingest.GenerateFrameFile(ingest.FrameGenOptions{
		Path: framesPath, Seed: 99, Frames: total,
		Truncated: true, TruncateIndex: boundaryIndex, OutOfOrder: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	faults := &model.Collector{}
	metas, err := ingest.ScanFrameFile(framesPath, faults)
	if err != nil {
		t.Fatal(err)
	}
	if fx.Truncated == nil {
		t.Fatal("missing truncation fixture location")
	}
	if got := faults.Count(model.FaultTruncated); got != 1 {
		t.Fatalf("truncated faults=%d want 1", got)
	}
	var trunc model.Fault
	for _, f := range faults.All() {
		if f.Kind == model.FaultTruncated {
			trunc = f
		}
	}
	if trunc.Offset != fx.Truncated.Offset {
		t.Fatalf("location error=%d want exact boundary offset", trunc.Offset-fx.Truncated.Offset)
	}
	rep := recovery.RestoreFrames(metas, total, faults)
	if rep.Frames[boundaryIndex].State != recovery.FrameMissing {
		t.Fatalf("boundary frame state=%s want missing", rep.Frames[boundaryIndex].State)
	}
	if rep.Frames[boundaryIndex+1].State != recovery.FramePresent {
		t.Fatal("first frame of next chunk must remain readable")
	}
	if ChunkCount(total, chunk) != 3 {
		t.Fatalf("chunks=%d want 3", ChunkCount(total, chunk))
	}
	// Chunk ranges stay frame aligned: [8,16) contains the damaged last slot,
	// while [16,24) starts at the following record.
	if boundaryIndex/chunk != 1 || (boundaryIndex+1)%chunk != 0 {
		t.Fatal("test index is not a chunk boundary")
	}
}
