package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"medpipe/internal/clock"
	"medpipe/internal/sample"
	"medpipe/internal/stream"
)

var ErrInterrupted = errors.New("pipeline interrupted after durable checkpoint")

// Run streams the source through the bounded pipe and completes all outputs.
func (r *Runner) Run(ctx context.Context) (*Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	f, err := os.Open(sourcePath(r.opts.OutDir))
	if err != nil {
		return nil, fmt.Errorf("open source: %w", err)
	}
	defer f.Close()
	reader, err := sample.NewReader(f)
	if err != nil {
		return nil, err
	}
	if r.state.NextSourceOffset > int64(len(sample.FileMagic)) {
		if err := reader.SeekChunk(r.state.NextSourceOffset); err != nil {
			return nil, fmt.Errorf("seek resume offset %d: %w", r.state.NextSourceOffset, err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			chunk, err := reader.ReadChunk()
			if errors.Is(err, io.EOF) {
				_ = r.pipe.Submit(ctx, stream.Envelope{Kind: "eof", ByteSize: 1, SourceOffset: reader.Offset()})
				return
			}
			if err != nil {
				_ = r.pipe.Submit(ctx, stream.Envelope{
					Kind: "read-error", ByteSize: 1, SourceOffset: chunk.Offset, Payload: err,
				})
				_ = r.pipe.Submit(ctx, stream.Envelope{Kind: "eof", ByteSize: 1, SourceOffset: reader.Offset()})
				return
			}
			env := stream.Envelope{
				Kind: "source-chunk", Sequence: chunk.Offset,
				ByteSize: int64(8 + len(chunk.Data)), SourceOffset: chunk.Offset, Payload: chunk,
			}
			if err := r.pipe.Submit(ctx, env); err != nil {
				return // context canceled during graceful interruption
			}
		}
	}()

	var runErr error
	var result *Result
	interrupted := false
	for {
		env, err := r.pipe.Receive(ctx)
		if err != nil {
			runErr = err
			cancel()
			break
		}

		if env.Kind == "eof" {
			if err := r.flushReorderAndSegments(ctx); err != nil {
				runErr = err
			} else {
				result, runErr = r.completeOutputs()
			}
			r.pipe.Ack(env)
			break
		}

		if d := r.opts.ConsumerDelay; d > 0 {
			timer := time.NewTimer(d)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				r.pipe.Ack(env)
				runErr = ctx.Err()
			}
			if runErr != nil {
				break
			}
		}

		if env.Kind == "read-error" {
			parseErr, _ := env.Payload.(error)
			r.recordParseError(clock.Wrap(clock.CodeInvalidPacket,
				&clock.Location{ByteOffset: env.SourceOffset}, parseErr, "source chunk read failed"))
		} else if chunk, ok := env.Payload.(sample.Chunk); ok {
			if err := r.processChunk(chunk); err != nil {
				runErr = err
				r.pipe.Ack(env)
				cancel()
				break
			}
			r.state.ProcessedChunks++
			r.state.NextSourceOffset = env.SourceOffset + env.ByteSize
		}

		every := int64(r.opts.CheckpointEvery)
		shouldCheckpoint := every > 0 && r.state.ProcessedChunks%every == 0
		if r.opts.InterruptAfterChunks > 0 && r.state.ProcessedChunks >= r.opts.InterruptAfterChunks {
			shouldCheckpoint = true
			interrupted = true
		}
		if shouldCheckpoint && runErr == nil {
			if err := r.saveCheckpoint(false, ""); err != nil {
				runErr = err
				r.pipe.Ack(env)
				cancel()
				break
			}
		}
		r.pipe.Ack(env)

		if interrupted {
			runErr = ErrInterrupted
			cancel()
			break
		}
	}

	cancel()
	wg.Wait()
	if runErr != nil && errors.Is(runErr, ErrInterrupted) {
		res := r.buildResult("", "")
		res.Interrupted = true
		res.Status = "interrupted_checkpoint_saved"
		return res, ErrInterrupted
	}
	if runErr != nil {
		return r.buildResult("", ""), runErr
	}
	return result, nil
}
