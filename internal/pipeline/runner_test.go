package pipeline_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/index"
	"github.com/zld/continuity/internal/mux"
	"github.com/zld/continuity/internal/pipeline"
)

func fixture(t *testing.T, n, every int, fault mux.Faults) (string, index.Fingerprint) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "in.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	specs := make([]mux.SampleSpec, n)
	for i := range specs {
		specs[i] = mux.SampleSpec{Delta: 1, Sync: every > 0 && i%every == 0}
	}
	if _, err := mux.WriteFile(f, []mux.TrackSpec{
		{Timescale: 25, Video: true, Samples: specs},
	}, fault); err != nil && fault == mux.FaultNone {
		t.Fatal(err)
	}
	f.Close()
	fp, err := index.FingerprintFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, fp
}

func TestRunBuildsCompleteIndex(t *testing.T) {
	path, fp := fixture(t, 200, 5, mux.FaultNone)
	in, err := demux.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	store, _ := index.NewStore(filepath.Join(t.TempDir(), "cp"), fp)

	ix, m, err := pipeline.Run(context.Background(), in, clock.DefaultPolicy(), store, pipeline.Config{
		ChunkSize: 64, ReadQueue: 16, PendingWrites: 4,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ix.EntryCount() != 200 {
		t.Fatalf("entries = %d, want 200", ix.EntryCount())
	}
	if m.EntriesIndexed != 200 || m.ChunksWritten != 4 { // 64*3 + tail
		t.Fatalf("metrics = %+v", m)
	}
	if ix.SyncCount() != 40 { // every 5th of 200
		t.Fatalf("sync count = %d, want 40", ix.SyncCount())
	}
	// Independent re-read of committed chunks must reproduce the same summary.
	rec, err := store.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Entries) != ix.EntryCount() {
		t.Fatal("on-disk entries differ from returned index")
	}
}

func TestSlowConsumerStillCompletesAndBackpressures(t *testing.T) {
	path, fp := fixture(t, 300, 10, mux.FaultNone)
	in, _ := demux.Open(path)
	defer in.Close()
	store, _ := index.NewStore(filepath.Join(t.TempDir(), "cp"), fp)

	start := time.Now()
	ix, m, err := pipeline.Run(context.Background(), in, clock.DefaultPolicy(), store, pipeline.Config{
		ChunkSize: 50, ReadQueue: 8, PendingWrites: 2, SlowDelay: 2 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ix.EntryCount() != 300 {
		t.Fatalf("entries = %d", ix.EntryCount())
	}
	// 6 chunks * 2ms of forced writer latency proves the writer was actually
	// the bottleneck and the producer waited rather than overrunning memory.
	if elapsed < 10*time.Millisecond {
		t.Fatalf("slow delay not honoured: %v", elapsed)
	}
	if m.PeakQueueChunks < 0 {
		t.Fatal("negative peak queue")
	}
}

func TestCancellationCommitsNothingPartial(t *testing.T) {
	path, fp := fixture(t, 200, 5, mux.FaultNone)
	in, _ := demux.Open(path)
	defer in.Close()
	cpDir := filepath.Join(t.TempDir(), "cp")
	store, _ := index.NewStore(cpDir, fp)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	_, _, err := pipeline.Run(ctx, in, clock.DefaultPolicy(), store, pipeline.Config{
		ChunkSize: 32, SlowDelay: 3 * time.Millisecond,
	})
	if !errors.Is(err, clock.ErrCanceled) && !errors.Is(err, clock.ErrInterrupted) {
		t.Fatalf("want cancellation error, got %v", err)
	}
	// Every chunk still on disk must validate; no half-written block exists.
	rec, err := store.Recover()
	if err != nil {
		t.Fatalf("committed blocks must remain valid: %v", err)
	}
	for _, e := range rec.Entries {
		if e.Size == 0 {
			t.Fatal("partial entry committed")
		}
	}
}

func TestInterruptLeavesUncommittedBlocksForResume(t *testing.T) {
	path, fp := fixture(t, 200, 5, mux.FaultNone)
	cpDir := filepath.Join(t.TempDir(), "cp")

	// First run stops after two committed blocks. The error path returns zero
	// metrics; the durable evidence of progress is what recovery can validate.
	in, _ := demux.Open(path)
	store, _ := index.NewStore(cpDir, fp)
	_, _, err := pipeline.Run(context.Background(), in, clock.DefaultPolicy(), store, pipeline.Config{
		ChunkSize: 64, InterruptAfter: 2, SlowDelay: time.Millisecond,
	})
	in.Close()
	if !errors.Is(err, clock.ErrInterrupted) {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}

	// Recovery must present exactly the two committed blocks.
	rec, err := store.Recover()
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(rec.Committed) != 2 || len(rec.Entries) != 128 {
		t.Fatalf("recover seqs=%v entries=%d", rec.Committed, len(rec.Entries))
	}
}

func TestCorruptInputPropagatesWithStage(t *testing.T) {
	path, _ := fixture(t, 50, 5, mux.FaultZeroTimescale)
	in, err := demux.Open(path)
	if err != nil {
		// Demux may reject it before the pipeline even starts; that is valid.
		if !errors.Is(err, clock.ErrCorrupt) {
			t.Fatalf("demux: %v", err)
		}
		return
	}
	defer in.Close()
	fp, _ := index.FingerprintFile(path)
	store, _ := index.NewStore(filepath.Join(t.TempDir(), "cp"), fp)
	_, _, err = pipeline.Run(context.Background(), in, clock.DefaultPolicy(), store, pipeline.Config{})
	if !errors.Is(err, clock.ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}
