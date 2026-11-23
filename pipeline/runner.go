// Package pipeline wires manifest parsing, analysis, caching and checkpoint
// recovery together. Results leave through a bounded queue so a slow consumer
// applies back pressure to the producer, and every segment is committed to
// the checkpoint only after it is fully analysed and accepted.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"mediamux/analyze"
	"mediamux/cache"
	"mediamux/media"
	"mediamux/stream"
)

// Runner executes one analysis pass over an Input.
type Runner struct {
	opts  Options
	cache cacheBackend
	queue *stream.Queue[SegmentOutcome]
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
	cacheLimit := opts.CacheLimit
	if cacheLimit <= 0 {
		if opts.Dir != "" {
			// On-disk bodies do not consume the streaming buffer budget.
			cacheLimit = 64 << 20
		} else {
			cacheLimit = opts.MemoryLimit
		}
	}
	if opts.Dir != "" {
		ds, err := cache.NewDiskStore(opts.Dir, cacheLimit)
		if err != nil {
			return nil, err
		}
		r.cache = ds
	} else {
		ms, err := cache.NewStore(cacheLimit)
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
	segCount, err := inputSegmentCount(in)
	if err != nil {
		return nil, err
	}
	if segCount != len(in.Manifest.Segments) {
		return nil, media.NewError(media.CodeInput, media.PhasePipeline,
			"segment payload count does not match manifest").AtOffset(0)
	}
	if len(in.Nodes) != segCount {
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
		ManifestDigest: manifestDigest, InputDigest: inputDigest, MaxSegments: segCount,
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

	end := segCount
	if r.opts.ProcessSegments > 0 {
		limit := decision.FromSegment + r.opts.ProcessSegments
		if limit < end {
			end = limit
			res.Events = append(res.Events, Event{Kind: "simulated_interrupt", Segment: end,
				Detail: "stopping at the last complete segment to exercise resume"})
		}
	}

	producerErr := make(chan error, 1)
	producerDone := make(chan struct{})
	go func() {
		err := r.produce(ctx, in, cp, decision.FromSegment, end, res)
		close(producerDone)
		producerErr <- err
	}()
	collectErr := r.collect(ctx, res, producerDone)
	prodErr := <-producerErr
	slow := prodErr != nil && stream.IsSlowConsumer(prodErr)
	if collectErr != nil {
		return nil, collectErr
	}
	if prodErr != nil && !slow {
		return nil, prodErr
	}
	if slow {
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

// inputSegmentCount validates that exactly one payload form is supplied and
// that it is parallel to the manifest.
func inputSegmentCount(in *Input) (int, error) {
	hasInline := len(in.Segments) > 0
	hasStream := len(in.Sources) > 0
	switch {
	case !hasInline && !hasStream:
		return 0, media.NewError(media.CodeInput, media.PhasePipeline,
			"no segment payloads or sources").AtOffset(0)
	case hasInline && hasStream:
		return 0, media.NewError(media.CodeInput, media.PhasePipeline,
			"both inline payloads and sources supplied; use one").AtOffset(0)
	}
	n := len(in.Segments)
	if hasStream {
		n = len(in.Sources)
	}
	if n != len(in.Manifest.Segments) {
		return 0, media.NewError(media.CodeInput, media.PhasePipeline,
			"segment payload count does not match manifest").AtOffset(0)
	}
	return n, nil
}

// checkCapacity rejects inputs whose segment working set cannot fit under the
// configured memory ceiling. One segment is held for analysis while up to
// QueueDepth results are queued. Streamed sources declare their size through
// SourceSizes so the same budget applies before any read happens.
func (r *Runner) checkCapacity(in *Input) error {
	var maxSeg int64
	for _, s := range in.Segments {
		if int64(len(s)) > maxSeg {
			maxSeg = int64(len(s))
		}
	}
	for _, size := range in.SourceSizes {
		if size > maxSeg {
			maxSeg = size
		}
	}
	budget := maxSeg * int64(r.opts.QueueDepth+1)
	if budget > r.opts.MemoryLimit {
		return media.NewError(media.CodeInput, media.PhasePipeline,
			fmt.Sprintf("segment working set %d exceeds memory limit %d", budget, r.opts.MemoryLimit)).AtOffset(0)
	}
	return nil
}

// produce analyses each segment, queues the outcome and, only after the
// consumer accepted it, commits it to the checkpoint. The checkpoint therefore
// never names a segment that was not delivered in full.
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
		// Hand the outcome to the consumer first. Only once it has been
		// accepted is the segment complete, so a blocked producer leaves the
		// checkpoint on the last segment the consumer actually took.
		if err := r.queue.Push(ctx, out); err != nil {
			return err
		}
		if err := cp.CommitSegment(i, out.Digest, out.Bytes); err != nil {
			return err
		}
		cp.SetStage(fmt.Sprintf("segment-%d", i), StageComplete)
		if err := r.saveCheckpoint(cp); err != nil {
			return err
		}
		r.muAppendEvents(res, events)
	}
	cp.SetStage("producer", StageComplete)
	return r.saveCheckpoint(cp)
}

// collect drains the queue into the result. The consumer may be slowed down
// deliberately, which propagates back pressure to the producer.
func (r *Runner) collect(ctx context.Context, res *Result, producerDone <-chan struct{}) error {
	if r.opts.StallBeforeFirst && r.opts.StallMS > 0 {
		if err := r.stallUntilBlocked(ctx, producerDone); err != nil {
			return err
		}
	}
	for {
		out, err := r.queue.Pop(ctx)
		if err != nil {
			if stream.IsDrained(err) {
				return nil
			}
			return err
		}
		res.Segments = append(res.Segments, out)
		if r.opts.ConsumerDelayMS > 0 {
			select {
			case <-ctx.Done():
				return media.NewError(media.CodeCanceled, media.PhasePipeline, "consumer canceled").Because(ctx.Err())
			case <-time.After(durationMS(r.opts.ConsumerDelayMS)):
			}
		}
	}
}

// stallUntilBlocked holds deliveries until the producer has actually tripped
// its slow-consumer budget. Because this consumer has not taken anything, a
// bounded queue of depth d fills after d segments and the producer must block;
// waiting for the queue's own TimedOut counter therefore makes the outcome
// deterministic rather than dependent on analysis speed.
func (r *Runner) stallUntilBlocked(ctx context.Context, producerDone <-chan struct{}) error {
	if r.opts.SlowConsumerMS <= 0 {
		// Without a producer budget nothing can time out, so there is no
		// slow-consumer verdict to produce.
		return nil
	}
	// Leave headroom over the budget so the producer has time to reach and
	// exceed it; the queue itself reports the exact moment it gives up.
	deadline := time.Now().Add(durationMS(r.opts.StallMS) + 10*time.Second)
	for r.queue.Stats().TimedOut == 0 {
		select {
		case <-ctx.Done():
			return media.NewError(media.CodeCanceled, media.PhasePipeline,
				"consumer canceled while waiting for back pressure").Because(ctx.Err())
		case <-producerDone:
			// The producer finished without ever blocking long enough.
			return nil
		case <-time.After(200 * time.Microsecond):
			if time.Now().After(deadline) {
				return nil
			}
		}
	}
	return nil
}

// designPeak is the deterministic upper bound on live media bytes: one
// segment under analysis plus QueueDepth outcomes waiting for the consumer.
// It is exactly the budget checkCapacity enforces, so reporting it cannot
// drift between runs that see identical input.
func (r *Runner) designPeak(in *Input) int64 {
	var maxSeg int64
	for _, s := range in.Segments {
		if int64(len(s)) > maxSeg {
			maxSeg = int64(len(s))
		}
	}
	for _, size := range in.SourceSizes {
		if size > maxSeg {
			maxSeg = size
		}
	}
	return maxSeg * int64(r.opts.QueueDepth+1)
}

// QueueStats exposes the bounded queue counters for diagnostics.
func (r *Runner) QueueStats() stream.QueueStats { return r.queue.Stats() }

// digestInput fingerprints the bytes this run will consume. Inline payloads
// are hashed in order; streamed sources fall back to their declared sizes and
// positions because they have not been read at resume-validation time.
func digestInput(in *Input) string {
	h := analyze.Digest([]byte(in.ManifestRaw))
	if len(in.Segments) > 0 {
		for _, seg := range in.Segments {
			h = analyze.Digest([]byte(h + analyze.Digest(seg)))
		}
		return h
	}
	for i, size := range in.SourceSizes {
		h = analyze.Digest([]byte(fmt.Sprintf("%s:%d:%d", h, i, size)))
	}
	return h
}
