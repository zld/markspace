package index_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/index"
)

func TestChunkCommitCheckpointAndRestart(t *testing.T) {
	root := t.TempDir()
	st, err := index.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	rec1 := index.ChunkRecord{ChunkID: "v", TrackID: 1, ChunkIndex: 0, SampleCount: 8, PayloadBytes: 640}
	if _, err := st.CommitChunk(rec1); err != nil {
		t.Fatal(err)
	}
	rec2 := rec1
	rec2.ChunkIndex = 1
	if _, err := st.CommitChunk(rec2); err != nil {
		t.Fatal(err)
	}
	cp := st.Checkpoint()
	if cp.LastSequence != 2 || cp.TotalSamples != 16 || cp.TotalPayloadBytes != 1280 {
		t.Fatalf("checkpoint=%+v", cp)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "chunks", "*.tmp")); len(matches) != 0 {
		t.Fatalf("temporary records left: %v", matches)
	}

	reopened, err := index.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Checkpoint().TotalSamples; got != 16 {
		t.Fatalf("reopen total=%d", got)
	}
}

func TestDeleteLastCheckpointFallsBackWithoutDoubleCount(t *testing.T) {
	root := t.TempDir()
	st, _ := index.Open(root)
	for i := 0; i < 3; i++ {
		_, err := st.CommitChunk(index.ChunkRecord{
			ChunkID: "a", TrackID: 9, ChunkIndex: i, SampleCount: 2, PayloadBytes: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	last := filepath.Join(root, "checkpoints", "000003.json")
	if err := os.Remove(last); err != nil {
		t.Fatal(err)
	}
	recovered, err := index.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	cp := recovered.Checkpoint()
	if cp.LastSequence != 2 || cp.TotalSamples != 4 || cp.TotalPayloadBytes != 20 {
		t.Fatalf("fallback cp=%+v", cp)
	}
	_, err = recovered.CommitChunk(index.ChunkRecord{
		ChunkID: "a", TrackID: 9, ChunkIndex: 2, SampleCount: 2, PayloadBytes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	after := recovered.Checkpoint()
	if after.LastSequence != 3 || after.TotalSamples != 6 || after.TotalPayloadBytes != 30 {
		t.Fatalf("replayed chunk double-counted: %+v", after)
	}
}
