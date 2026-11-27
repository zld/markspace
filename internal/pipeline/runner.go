// Package pipeline wires demux, repair, index and checkpoint storage into one
// bounded, cancellable flow. Every stage communicates through capacity-limited
// channels so a slow writer applies back-pressure all the way to the reader and
// memory use stays a function of the configured queue and chunk sizes.
package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/index"
)
// Config bounds the in-flight work. Zero values are replaced with safe defaults.
type Config struct {
	// ReadQueue caps entries produced ahead of the chunker.
	ReadQueue int
	// ChunkSize is the number of index entries per checkpoint block.
	ChunkSize int
	// PendingWrites caps fully built blocks waiting for the writer.
	PendingWrites int
	// SlowDelay makes the writer sleep before each block, modelling a disk
	// that cannot keep up; zero disables it.
	SlowDelay time.Duration
	// InterruptAfter > 0 stops the run deterministically before writing that
	// many blocks, leaving every later block uncommitted (fault injection).
	InterruptAfter int
	// Skip, when non-nil, reports a block sequence already committed by a
	// previous run; the writer leaves it untouched instead of rewriting it.
	Skip func(seq uint64) bool
}

// Metrics reports observed pipeline behaviour for the acceptance summary.
type Metrics struct {
	ChunksWritten   int
	EntriesIndexed  int
	PeakQueueChunks int
}

func (c *Config) defaults() {
	if c.ReadQueue <= 0 {
		c.ReadQueue = 16
	}
	if c.ChunkSize <= 0 {
		c.ChunkSize = 64
	}
	if c.PendingWrites <= 0 {
		c.PendingWrites = 4
	}
}

// Run drives the full flow and returns the index built from committed blocks.
// On cancellation, interruption or write failure no partial block is marked
// committed: the checkpoint store only exposes blocks that finished its atomic
// write, and the returned error names the pipeline stage.
func Run(ctx context.Context, in *demux.File, policy clock.Policy,
	store *index.Store, cfg Config) (*index.Index, Metrics, error) {
	cfg.defaults()

	entries := make(chan index.Entry, cfg.ReadQueue)
	pending := make(chan index.Chunk, cfg.PendingWrites)

	var peak int32
	var peakMu sync.Mutex
	noteQueue := func() {
		q := len(entries)/cfg.ChunkSize + len(pending)
		peakMu.Lock()
		if int32(q) > peak {
			peak = int32(q)
		}
		peakMu.Unlock()
	}

	writerErr := make(chan error, 1)
	go func() {
		written := 0
		for ch := range pending {
			// A block validated during resume is reusable work: do not rewrite
			// it, but still count it so InterruptAfter measures new work.
			if cfg.Skip != nil && cfg.Skip(ch.Seq) {
				written++
				continue
			}
			if cfg.InterruptAfter > 0 && written >= cfg.InterruptAfter {
				writerErr <- clock.ErrInterrupted
				return
			}
			if cfg.SlowDelay > 0 {
				select {
				case <-ctx.Done():
					writerErr <- clock.ErrCanceled
					return
				case <-time.After(cfg.SlowDelay):
				}
			}
			if err := store.Write(ch); err != nil {
				writerErr <- err
				return
			}
			written++
		}
		writerErr <- nil
	}()

	// Chunker groups entries into fixed-size blocks with a contiguous sequence.
	chunkerDone := make(chan struct{})
	go func() {
		defer close(chunkerDone)
		var block []index.Entry
		nextSeq := uint64(0)
		flush := func() bool {
			if len(block) == 0 {
				return true
			}
			ch := index.Chunk{Seq: nextSeq, Fingerprint: store.Fingerprint(), Entries: block}
			nextSeq++
			block = nil
			noteQueue()
			select {
			case pending <- ch:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			select {
			case e, ok := <-entries:
				if !ok {
					flush()
					return
				}
				block = append(block, e)
				if len(block) == cfg.ChunkSize && !flush() {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Producer repairs each track and streams entries. A full entries channel
	// blocks here, so a slow writer pauses upstream reading (back-pressure).
	prodErr := produce(ctx, in, policy, entries, noteQueue)
	close(entries)
	<-chunkerDone
	close(pending)

	werr := <-writerErr
	if prodErr != nil {
		return nil, Metrics{}, prodErr
	}
	if werr != nil {
		return nil, Metrics{}, mapStageErr(werr)
	}

	// Re-read only the blocks that were actually committed, giving an index
	// whose summary is identical to a resume-based rebuild.
	rec, err := store.Recover()
	if err != nil {
		return nil, Metrics{}, clock.Wrap(clock.StageCheckpoint, "recover", err)
	}
	ix := index.New()
	ix.Add(rec.Entries)
	metrics := Metrics{
		ChunksWritten:   len(rec.Committed),
		EntriesIndexed:  len(rec.Entries),
		PeakQueueChunks: int(atomic.LoadInt32(&peak)),
	}
	return ix, metrics, nil
}

func mapStageErr(err error) error {
	if err == clock.ErrInterrupted || err == clock.ErrCanceled {
		return clock.Wrap(clock.StagePipeline, "run", err)
	}
	return clock.Wrap(clock.StageCheckpoint, "write", err)
}
