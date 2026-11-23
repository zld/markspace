// Package pipeline wires manifest parsing, analysis, caching and checkpoint
// recovery together. Results leave through a bounded queue so a slow consumer
// applies back pressure to the producer, and every segment is committed to
// the checkpoint only after it is fully analysed and accepted.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"mediamux/analyze"
	"mediamux/cache"
	"mediamux/media"
	"mediamux/stream"
)

// Runner executes one analysis pass over an Input.
type Runner struct {
	opts     Options
	cache    cacheBackend
	queue    *stream.Queue[SegmentOutcome]
	inflight atomic.Int64
	peak     atomic.Int64
}

// cacheBackend is the subset of store behaviour the runner needs.
type cacheBackend interface {
	Put(data []byte, p cache.Provenance) (*cache.Entry, error)
	PutWithMeta(data []byte, p cache.Provenance, meta []byte) (*cache.Entry, error)
	Lookup(key string, expect cache.Provenance) cache.LookupResult
	Rebuild(data []byte, p cache.Provenance) (*cache.Entry, error)
	Fetch(key string) (*cache.Entry, bool)
	Stats() cache.Stats
}

// NewRunner prepares a runner and its bounded output queue.
func NewRunner(opts Options) (*Runner, error) {
	if opts.MemoryLimit <= 0 {
		return nil, media.NewError(media.CodeInput, media.PhasePipeline, "memory limit must be positive").AtOffset(0)
	}
	if opts.QueueDepth <= 0 {
		opts.QueueDepth = 2
	}
	q, err := stream.NewQueue[SegmentOutcome](opts.QueueDepth, durationMS(opts.SlowConsumerMS))
	if err != nil {
		return nil, err
	}
	r := &Runner{opts: opts, queue: q}
	if opts.Dir != "" {
		ds, err := cache.NewDiskStore(opts.Dir, opts.MemoryLimit)
		if err != nil {
			return nil, err
		}
		r.cache = ds
	} else {
		ms, err := cache.NewStore(opts.MemoryLimit)
		if err != nil {
			return nil, err
		}
		r.cache = ms
	}
	return r, nil
}

// CacheStats exposes the store's counters for the terminal adapter.
func (r *Runner) CacheStats() cache.Stats { return r.cache.Stats() }

// Run processes the input from the resume point to the end (or simulated
// stop) and returns the aggregate result. Damaged media is diagnosed and
// returned without an error; only invariant failures return one.
func (r *Runner) Run(ctx context.Context, in *Input) (*Result, error) {
	if in == nil || in.Manifest == nil {
		return nil, media.NewError(media.CodeInput, media.PhasePipeline, "nil input").AtOffset(0)
	}
	if len(in.Segments) != len(in.Manifest.Segments) {
		return nil, media.NewError(media.CodeInput, media.PhasePipeline,
			"segment payload count does not match manifest").AtOffset(0)
	}
	if len(in.Nodes) != len(in.Segments) {
		return nil, media.NewError(media.CodeInput, media.PhasePipeline,
			"node count does not match segment count").AtOffset(0)
	}
	if err := r.checkCapacity(in); err != nil {
		return nil, err
	}
	res := &Result{}
	manifestDigest := analyze.Digest([]byte(in.ManifestRaw))
	inputDigest := digestInput(in)
	cp, err := Load(r.checkpointPath())
	if err != nil {
		return nil, err
	}
	decision, err := ValidateResume(cp, ResumeRequest{
		ManifestDigest: manifestDigest, InputDigest: inputDigest, MaxSegments: len(in.Segments),
	})
	if err != nil {
		if media.HasCode(err, media.CodeManifestChg) {
			res.Events = append(res.Events, Event{Kind: "checkpoint_reset", Segment: -1, Detail: err.Error()})
			cp = nil
			decision = ResumeDecision{FromSegment: 0, Reason: "manifest changed; full rerun"}
		} else {
			return nil, err
		}
	}
	if cp == nil {
		cp = NewCheckpoint(string(in.Manifest.Kind), manifestDigest, inputDigest, r.opts.Epoch)
	} else {
		cp.Epoch = r.opts.Epoch
	}
	cp.SetStage("parse", StageComplete)
	cp.SetStage("analyze", StageRunning)
	res.Resume = decision
	if decision.Resumed && decision.FromSegment > 0 {
		res.Events = append(res.Events, Event{Kind: "checkpoint_resume", Segment: decision.FromSegment, Detail: decision.Reason})
	}

	end := len(in.Segments)
	if r.opts.ProcessSegments > 0 {
		limit := decision.FromSegment + r.opts.ProcessSegments
		if limit < end {
			end = limit
			res.Events = append(res.Events, Event{Kind: "simulated_interrupt", Segment: end,
				Detail: "stopping at the last complete segment to exercise resume"})
		}
	}

	producerErr := make(chan error, 1)
	go func() {
		producerErr <- r.produce(ctx, in, cp, decision.FromSegment, end, res)
	}()
	collectErr := r.collect(ctx, res)

	if err := <-producerErr; err != nil {
		return nil, err
	}
	if collectErr != nil && !errors.Is(collectErr, stream.ErrSlowConsumer) {
		return nil, collectErr
	}
	if errors.Is(collectErr, stream.ErrSlowConsumer) {
		res.SlowConsumer = true
		res.Events = append(res.Events, Event{Kind: "slow_consumer", Segment: cp.Cursor,
			Detail: "consumer timed out; branch stopped at last complete checkpoint"})
		cp.SetStage("consumer", StageFailed)
	}
	cp.SetStage("analyze", StageComplete)
	cp.SetStage("cache", StageComplete)
	if !res.SlowConsumer {
		cp.SetStage("consumer", StageComplete)
	}
	if err := r.rehydrateResumed(res, cp); err != nil {
		return nil, err
	}
	r.finalise(res, cp, in)
	return res, nil
}

// rehydrateResumed restores outcomes for segments committed by an earlier run
// from their cached analysis records, so quality statistics and the result
// digest cover the whole timeline rather than only this run's suffix.
func (r *Runner) rehydrateResumed(res *Result, cp *Checkpoint) error {
	present := make(map[int]bool, len(res.Segments))
	for _, s := range res.Segments {
		present[s.Index] = true
	}
	indices := make([]int, 0, len(cp.SegmentDigests))
	for idx := range cp.SegmentDigests {
		if !present[idx] {
			indices = append(indices, idx)
		}
	}
	sort.Ints(indices)
	for _, idx := range indices {
		key := cp.SegmentDigests[idx]
		entry, ok := r.cache.Fetch(key)
		if !ok || len(entry.Meta) == 0 {
			return media.NewError(media.CodeCacheMiss, media.PhaseCache,
				fmt.Sprintf("resume cannot rehydrate segment %d analysis", idx)).
				AtOffset(int64(idx))
		}
		var out SegmentOutcome
		if err := json.Unmarshal(entry.Meta, &out); err != nil || out.Digest != key {
			return media.NewError(media.CodeCachePoison, media.PhaseCache,
				fmt.Sprintf("segment %d analysis record is unreadable", idx)).AtOffset(int64(idx))
		}
		out.FromCache = true
		res.Segments = append(res.Segments, out)
		res.Events = append(res.Events, Event{Kind: "checkpoint_rehydrate", Segment: idx,
			Detail: "restored committed analysis from verified cache record"})
	}
	sort.Slice(res.Segments, func(i, j int) bool { return res.Segments[i].Index < res.Segments[j].Index })
	return nil
}

// checkCapacity rejects inputs whose segment working set cannot fit under the
// configured memory ceiling. One segment is held for analysis while up to
// QueueDepth results are queued.
func (r *Runner) checkCapacity(in *Input) error {
	var maxSeg int64
	for _, s := range in.Segments {
		if int64(len(s)) > maxSeg {
			maxSeg = int64(len(s))
		}
	}
	budget := maxSeg * int64(r.opts.QueueDepth+1)
	if budget > r.opts.MemoryLimit {
		return media.NewError(media.CodeInput, media.PhasePipeline,
			fmt.Sprintf("segment working set %d exceeds memory limit %d", budget, r.opts.MemoryLimit)).AtOffset(0)
	}
	return nil
}

// produce analyses each segment, commits it to the checkpoint, then queues the
// outcome. Committing before enqueueing guarantees the checkpoint only ever
// names fully analysed segments.
func (r *Runner) produce(ctx context.Context, in *Input, cp *Checkpoint, start, end int, res *Result) error {
	defer r.queue.Close()
	for i := start; i < end; i++ {
		if err := ctx.Err(); err != nil {
			return media.NewError(media.CodeCanceled, media.PhasePipeline, "run canceled").AtOffset(int64(i)).Because(err)
		}
		out, events, err := r.processSegment(ctx, in, i)
		if err != nil {
			return err
		}
		if err := cp.CommitSegment(i, out.Digest, out.Bytes); err != nil {
			return err
		}
		cp.SetStage(fmt.Sprintf("segment-%d", i), StageComplete)
		if err := r.saveCheckpoint(cp); err != nil {
			return err
		}
		r.accountPeak(out.Bytes)
		if err := r.queue.Push(ctx, out); err != nil {
			return err
		}
		r.muAppendEvents(res, events)
	}
	cp.SetStage("producer", StageComplete)
	return r.saveCheckpoint(cp)
}

// collect drains the queue into the result. The consumer may be slowed down
// deliberately, which propagates back pressure to the producer.
func (r *Runner) collect(ctx context.Context, res *Result) error {
	for {
		out, err := r.queue.Pop(ctx)
		if err != nil {
			if stream.IsDrained(err) {
				return nil
			}
			return err
		}
		res.Segments = append(res.Segments, out)
		r.inflight.Add(-out.Bytes)
		if r.opts.ConsumerDelayMS > 0 {
			select {
			case <-ctx.Done():
				return media.NewError(media.CodeCanceled, media.PhasePipeline, "consumer canceled").Because(ctx.Err())
			case <-time.After(durationMS(r.opts.ConsumerDelayMS)):
			}
		}
	}
}

// accountPeak tracks the largest live working set: queued results plus the
// chunk the producer is currently holding.
func (r *Runner) accountPeak(bytes int64) {
	live := r.inflight.Add(bytes)
	for {
		peak := r.peak.Load()
		if live <= peak || r.peak.CompareAndSwap(peak, live) {
			break
		}
	}
}
