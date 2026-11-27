// Package pipeline runs a source through a chain of stages with a bounded
// chunk size and a bounded queue between stages. A slow consumer blocks the
// producer instead of growing memory, and cancelling the context makes every
// stage return the same cause.
package pipeline

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"archivecheck/internal/fault"
)

// Config bounds the pipeline's memory use. ChunkSize is the maximum number of
// source bytes handed to a stage at once; QueueSize is the number of chunks
// that may wait in front of each stage.
type Config struct {
	ChunkSize int
	QueueSize int
}

// DefaultConfig is used by the demo unless the caller overrides it.
func DefaultConfig() Config { return Config{ChunkSize: 4096, QueueSize: 2} }

// Hard ceilings that keep a caller from accidentally requesting an unbounded
// run.
const (
	MaxChunkSize = 8 << 20
	MaxQueueSize = 1024
)

// Validate rejects configurations that would either do no work or be
// unbounded.
func (c Config) Validate() error {
	if c.ChunkSize < 1 {
		return fault.New(fault.StagePipeline, fault.KindInvalid, "", 0, "chunk size must be at least 1 byte, got %d", c.ChunkSize)
	}
	if c.ChunkSize > MaxChunkSize {
		return fault.New(fault.StagePipeline, fault.KindLimit, "", 0, "chunk size %d exceeds the %d byte ceiling", c.ChunkSize, MaxChunkSize)
	}
	if c.QueueSize < 1 {
		return fault.New(fault.StagePipeline, fault.KindInvalid, "", 0, "queue size must be at least 1, got %d", c.QueueSize)
	}
	if c.QueueSize > MaxQueueSize {
		return fault.New(fault.StagePipeline, fault.KindLimit, "", 0, "queue size %d exceeds the %d entry ceiling", c.QueueSize, MaxQueueSize)
	}
	return nil
}

// Bound is the maximum number of chunks that can be alive at once for the
// given configuration and stage count: one in each stage plus one per queue.
func (c Config) Bound(stages int) int {
	return (stages + 1) * (c.QueueSize + 1)
}

// Chunk is one ordered slice of source bytes. Offset is the absolute position
// of Data[0] inside the source.
type Chunk struct {
	Index  int
	Offset int64
	Data   []byte
}

// Stage is one bounded processing step.
type Stage struct {
	Name string
	// Handle processes a chunk. Returning an error cancels the whole pipeline
	// with that error as the shared cause.
	Handle func(ctx context.Context, c Chunk) error
}

// StageResult records what one stage did.
type StageResult struct {
	Name string `json:"name"`
	// Chunks and Bytes count the work this stage completed.
	Chunks int   `json:"chunks"`
	Bytes  int64 `json:"bytes"`
	// Waits counts the times this stage had to block because the following
	// queue was full.
	Waits int `json:"waits"`
	// Err is the shared cancellation cause when the run did not complete.
	Err error `json:"-"`
}

// Report is the outcome of a run.
type Report struct {
	Stages []StageResult
	// BackpressureWaits counts every time the producer or a stage blocked on a
	// full queue, i.e. every time the pipeline pushed back on its input.
	BackpressureWaits  int
	PeakOutstanding    int
	ChunkSize          int
	QueueSize          int
	OutstandingCeiling int
}

// Cause returns the shared cancellation cause of the run, if any.
func (r Report) Cause() error {
	for _, s := range r.Stages {
		if s.Err != nil {
			return s.Err
		}
	}
	return nil
}

type counters struct {
	live  atomic.Int64
	peak  atomic.Int64
	waits atomic.Int64
}

func (c *counters) acquire() {
	n := c.live.Add(1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			return
		}
	}
}

func (c *counters) release() { c.live.Add(-1) }

func (c *counters) countWait() { c.waits.Add(1) }

// Run streams every byte of src through stages in order.
//
// The producer reads src in ChunkSize blocks and sends each block to the first
// stage through a queue of QueueSize entries. When a downstream consumer is
// slow the send blocks, which stops the source read; the number of live chunks
// is therefore bounded by Config.Bound. Cancellation propagates through one
// derived context so every stage observes the same cause.
//
// size, when positive, caps how many bytes are read from src.
func Run(ctx context.Context, src io.Reader, size int64, cfg Config, stages ...Stage) (Report, error) {
	rep := Report{
		Stages:             make([]StageResult, len(stages)),
		ChunkSize:          cfg.ChunkSize,
		QueueSize:          cfg.QueueSize,
		OutstandingCeiling: cfg.Bound(len(stages)),
	}
	if err := cfg.Validate(); err != nil {
		return rep, err
	}
	if len(stages) == 0 {
		return rep, fault.New(fault.StagePipeline, fault.KindInvalid, "", 0, "pipeline needs at least one stage")
	}
	for i := range stages {
		if stages[i].Name == "" {
			stages[i].Name = fmt.Sprintf("stage-%d", i)
		}
		if stages[i].Handle == nil {
			return rep, fault.New(fault.StagePipeline, fault.KindInvalid, "", 0, "stage %q has no handler", stages[i].Name)
		}
		rep.Stages[i].Name = stages[i].Name
	}

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	queues := make([]chan Chunk, len(stages))
	for i := range queues {
		queues[i] = make(chan Chunk, cfg.QueueSize)
	}

	var counter counters
	var wg sync.WaitGroup

	// Producer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(queues[0])
		var reader io.Reader = src
		if size > 0 {
			reader = io.LimitReader(src, size)
		}
		buf := make([]byte, cfg.ChunkSize)
		var offset int64
		index := 0
		for {
			if runCtx.Err() != nil {
				return
			}
			n, err := io.ReadFull(reader, buf)
			if n > 0 {
				chunk := Chunk{Index: index, Offset: offset, Data: append([]byte(nil), buf[:n]...)}
				counter.acquire()
				select {
				case queues[0] <- chunk:
					offset += int64(n)
					index++
					continue
				default:
					// The first queue is full: record that the producer had
					// to wait, then block until the consumer catches up.
					counter.countWait()
					select {
					case queues[0] <- chunk:
						offset += int64(n)
						index++
					case <-runCtx.Done():
						counter.release()
						return
					}
				}
			}
			if err != nil {
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					return
				}
				cancel(fault.Wrap(err, fault.StagePipeline, fault.KindCorrupt, "", offset, "read source at offset %d", offset))
				return
			}
		}
	}()

	// Stages.
	for i := range stages {
		i, stage := i, stages[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i+1 < len(stages) {
				defer close(queues[i+1])
			}
			res := &rep.Stages[i]
			for {
				select {
				case <-runCtx.Done():
					return
				case chunk, ok := <-queues[i]:
					if !ok {
						return
					}
					if runCtx.Err() != nil {
						counter.release()
						return
					}
					if err := stage.Handle(runCtx, chunk); err != nil {
						cancel(err)
						counter.release()
						return
					}
					res.Chunks++
					res.Bytes += int64(len(chunk.Data))
					if i+1 >= len(stages) {
						counter.release()
						continue
					}
					select {
					case queues[i+1] <- chunk:
					default:
						// The next queue is full: block here, which in turn
						// stops this stage reading from its own input and
						// eventually stalls the source reader. The chunk is
						// already counted as live since it was first queued.
						res.Waits++
						counter.countWait()
						select {
						case queues[i+1] <- chunk:
						case <-runCtx.Done():
							counter.release()
							return
						}
					}
				}
			}
		}()
	}

	wg.Wait()
	cause := context.Cause(runCtx)
	rep.PeakOutstanding = int(counter.peak.Load())
	rep.BackpressureWaits = int(counter.waits.Load())
	for i := range rep.Stages {
		rep.Stages[i].Err = cause
	}
	return rep, cause
}
