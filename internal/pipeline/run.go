// Package pipeline coordinates every subsystem: EBML/Matroska parsing, RTP
// association, time alignment, bounded streaming, content-addressable
// caching and checkpointed resume. It is deterministic so an interrupted
// run resumes to the same manifest and segment digests as a clean run.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/local/tracecheck/internal/align"
	"github.com/local/tracecheck/internal/cache"
	"github.com/local/tracecheck/internal/checkpoint"
	"github.com/local/tracecheck/internal/clock"
	"github.com/local/tracecheck/internal/manifest"
	"github.com/local/tracecheck/internal/mkv"
	"github.com/local/tracecheck/internal/rtptrace"
	"github.com/local/tracecheck/internal/stream"
)

// LayerSpec maps one requested bitrate to a Matroska track number.
type LayerSpec struct {
	Bitrate     uint32
	TrackNumber uint64
}

// Config configures a run.
type Config struct {
	InputID        string
	SegmentNS      int64
	MaxSkewNS      clock.Tick
	QueueCapacity  int
	MaxItemBytes   int
	SlowConsumerMS int
	Layers         []LayerSpec
	CacheDir       string
	ResumeDir      string
}

// Report is the outcome of a run.
type Report struct {
	Manifest    *manifest.Manifest
	CacheHits   int
	CacheMisses int
	Mismatches  []cache.Mismatch
	QueuePeak   int
	Resumed     bool
}

// segmentWork is one unit streamed through the bounded queue.
type segmentWork struct {
	index   int
	startNS int64
	endNS   int64
	content []byte
}

// Run executes the full pipeline. On cancellation it returns a
// clock.CodeCancelled error but every checkpoint already committed remains
// valid, so a subsequent Run resumes from that point.
func Run(ctx context.Context, doc *mkv.Document, cfg Config) (*Report, error) {
	trace := rtptrace.FromBlocks(doc.Blocks)
	layers := make([]align.Layer, 0, len(cfg.Layers))
	for _, l := range cfg.Layers {
		layers = append(layers, align.Layer{Bitrate: l.Bitrate, TrackNumber: l.TrackNumber})
	}
	result := align.Cut(doc.SortedBlocks(), trace, align.Config{
		SegmentNS: cfg.SegmentNS,
		MaxSkewNS: cfg.MaxSkewNS,
		Layers:    layers,
	})

	store, err := checkpoint.OpenStore(cfg.ResumeDir)
	if err != nil {
		return nil, err
	}
	state, err := store.Load(cfg.InputID)
	if err != nil {
		return nil, err
	}
	cas, err := cache.Open(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	queue := stream.NewBounded(cfg.QueueCapacity, cfg.MaxItemBytes)

	contentByIndex := materialize(result, doc)
	report := &Report{Resumed: len(state.Segments) > 0}
	contentRefs := map[int]manifest.ContentRef{}
	// Reuse digests of segments completed before an interruption. Snapshot
	// the committed count up front: the producer goroutine must not read
	// state.Segments while the consumer mutates it via Advance.
	committed := len(state.Segments)
	for _, rec := range state.Segments {
		contentRefs[rec.Index] = manifest.ContentRef{SHA256: rec.Digest, CacheKey: rec.CacheKey}
	}

	producerErr := make(chan error, 1)
	go func() {
		producerErr <- produce(ctx, queue, result, contentByIndex, committed)
	}()

	consumeErr := consume(ctx, queue, store, state, cas, cfg, result, contentRefs, report)
	perr := <-producerErr
	if consumeErr != nil {
		return nil, consumeErr
	}
	if perr != nil {
		return nil, perr
	}

	report.QueuePeak = queue.Peak()
	report.CacheHits = cas.Hits
	report.CacheMisses = cas.Misses
	report.Mismatches = cas.Mismatches
	report.Manifest = manifest.Build(manifest.Inputs{
		InputID:        cfg.InputID,
		Doc:            doc,
		Trace:          trace,
		Result:         result,
		QueuePeak:      report.QueuePeak,
		CacheHits:      cas.Hits,
		SegmentContent: contentRefs,
	})
	if err := store.Finalize(state, report.Manifest.ManifestSHA256); err != nil {
		return nil, err
	}
	return report, nil
}

// produce streams every not-yet-checkpointed segment into the bounded queue.
// committedCount is the snapshot of completed segments taken before the
// consumer starts mutating checkpoint state.
func produce(ctx context.Context, q *stream.Bounded, result *align.Result,
	content map[int][]byte, committedCount int) error {
	defer q.Close()
	for _, seg := range result.Segments {
		if seg.Index < committedCount {
			continue // already committed in a previous run
		}
		item := stream.Item{
			SegmentIndex: seg.Index,
			SourceStart:  seg.StartNS,
			SourceEnd:    seg.EndNS,
			Content:      content[seg.Index],
		}
		if err := q.Put(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

// consume drains the queue, verifying cache provenance and advancing the
// checkpoint only after a segment is fully handled and digest-verified.
func consume(ctx context.Context, q *stream.Bounded, store *checkpoint.Store,
	state *checkpoint.State, cas *cache.CAS, cfg Config, result *align.Result,
	refs map[int]manifest.ContentRef, report *Report) error {
	for {
		item, ok, err := q.Get(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if cfg.SlowConsumerMS > 0 {
			select {
			case <-time.After(time.Duration(cfg.SlowConsumerMS) * time.Millisecond):
			case <-ctx.Done():
				return clock.NewError(clock.CodeCancelled, item.SourceStart, "", ctx.Err().Error())
			}
		}
		seg := result.Segments[item.SegmentIndex]
		src := cache.Source{InputID: cfg.InputID, Start: item.SourceStart, End: item.SourceEnd}
		key := cache.Key(item.Content)
		if _, mismatch := cas.Lookup(src, item.Content); mismatch != nil {
			report.Mismatches = append(report.Mismatches, *mismatch)
		}
		if _, err := cas.Put(src, item.Content); err != nil {
			return err
		}
		refs[seg.Index] = manifest.ContentRef{SHA256: key, CacheKey: key}
		// A segment is committed only once its content digest is verified
		// and its record written; failed-alignment segments keep their flag
		// but are still durably recorded for deterministic resume.
		if err := store.Advance(state, checkpoint.SegmentRecord{
			Index: seg.Index, StartNS: seg.StartNS, EndNS: seg.EndNS,
			Digest: key, CacheKey: key,
		}); err != nil {
			return err
		}
	}
}

// materialize builds deterministic segment content by concatenating block
// payloads in sorted timeline order. The byte layout is stable across runs.
func materialize(result *align.Result, doc *mkv.Document) map[int][]byte {
	out := map[int][]byte{}
	offsetToPayload := map[int64][]byte{}
	for _, b := range doc.Blocks {
		offsetToPayload[b.SourceOffset] = b.Payload
	}
	for _, seg := range result.Segments {
		var buf []byte
		for _, off := range seg.BlockSourceOffsets {
			buf = append(buf, offsetToPayload[off]...)
		}
		out[seg.Index] = buf
	}
	return out
}

// InputDigest returns the sha256 hex digest of raw input bytes, used as the
// stable input identity for cache and checkpoint binding.
func InputDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
