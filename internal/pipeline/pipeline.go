// Package pipeline wires the recovery stages: bounded streaming analysis,
// keyframe index validation, per-layer recovery, shared-clock merge, bitrate
// measurement, manifest rendering and invariant verification.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"archivecheck/internal/bitrate"
	"archivecheck/internal/index"
	"archivecheck/internal/manifest"
	"archivecheck/internal/media"
	"archivecheck/internal/recovery"
	"archivecheck/internal/sample"
	"archivecheck/internal/stream"
	"archivecheck/internal/verify"
)

// Config is one demo run.
type Config struct {
	Case            string
	Seed            int64
	ChunkBytes      int
	Queue           int
	Targets         []float64
	ConsumerDelayMs int
	ToleranceMs     int64
	WindowMs        int64
}

// Output is everything the CLI renders.
type Output struct {
	Manifest     manifest.Document
	Verify       verify.Report
	Damaged      bool
	PeakBuffered int64
	QueueBound   int64
	Drained      bool
}

// Run executes the full demonstration pipeline for one fixture.
func Run(ctx context.Context, cfg Config) (Output, error) {
	if err := sample.ValidateCase(cfg.Case); err != nil {
		return Output{}, err
	}
	if cfg.ChunkBytes <= 0 || cfg.Queue <= 0 || cfg.ToleranceMs <= 0 || cfg.WindowMs <= 0 {
		return Output{}, errors.New("pipeline: chunk-bytes, queue, tolerance-ms and window-ms must be positive")
	}
	if cfg.ConsumerDelayMs < 0 {
		return Output{}, errors.New("pipeline: consumer delay must not be negative")
	}
	archive, err := sample.Generate(cfg.Case, cfg.Seed, cfg.ChunkBytes, cfg.Targets)
	if err != nil {
		return Output{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	consumers := make([]stream.Consumer, 0, len(archive.Layers))
	analyzers := make([]*analyzer, 0, len(archive.Layers))
	for _, l := range archive.Layers {
		a, err := newAnalyzer(l.Name, l.Codec, media.MustClock(l.Timescale), cfg.ConsumerDelayMs, cfg.ChunkBytes, len(l.Stream))
		if err != nil {
			return Output{}, err
		}
		analyzers = append(analyzers, a)
		consumers = append(consumers, a)
	}

	// One bounded reader owns all buffering. Layers are multiplexed into it,
	// the incremental demux splits them back out, and the fan-out applies the
	// per-layer queue bound with backpressure.
	layerNames := make([]string, 0, len(archive.Layers))
	for _, l := range archive.Layers {
		layerNames = append(layerNames, l.Name)
	}
	mux, err := stream.NewMuxSource(newRouter(archive.Layers, cfg.ChunkBytes), layerNames)
	if err != nil {
		return Output{}, err
	}
	reader, err := stream.Open(mux, stream.Options{ChunkBytes: cfg.ChunkBytes, Queue: cfg.Queue})
	if err != nil {
		return Output{}, err
	}
	demux, err := stream.NewDemux(reader, layerNames)
	if err != nil {
		_ = reader.Close()
		return Output{}, err
	}
	fan, err := stream.NewFanout(ctx, demux, cfg.Queue, consumers...)
	if err != nil {
		_ = reader.Close()
		return Output{}, err
	}
	fan.Wait()
	peak, bound := fan.PeakQueued(), fan.Bound()
	closeErr := fan.Close()
	if err := ctx.Err(); err != nil {
		return Output{}, fmt.Errorf("pipeline: fan-out canceled: %w", err)
	}
	if closeErr != nil {
		return Output{}, fmt.Errorf("pipeline: fan-out: %w", closeErr)
	}

	var results []recovery.Result
	var faults []string
	for i, l := range archive.Layers {
		a := analyzers[i]
		val := index.Validate(l.Index, int64(len(l.Stream)), a.boundaries, index.ValidationOptions{DriftTolerance: 4})
		r := recovery.Recover(recovery.Input{
			Layer: l.Name, Clock: media.MustClock(l.Timescale), StreamLen: int64(len(l.Stream)),
			DurationMs: l.DurationMs, Units: a.units, Truncation: a.truncation, Index: val,
		})
		r.Faults = append(r.Faults, val.Faults...)
		r.Faults = append(r.Faults, a.faults...)
		media.SortFaults(r.Faults)
		for _, f := range r.Faults {
			faults = append(faults, f.String())
		}
		results = append(results, r)
	}

	merge, err := recovery.MergeResult(results, recovery.MergeOptions{ToleranceMs: cfg.ToleranceMs, AlignWindowMs: 100})
	if err != nil {
		return Output{}, err
	}
	for _, f := range merge.Faults {
		faults = append(faults, f.String())
	}
	sort.Strings(faults)

	var layerInputs []manifest.LayerInput
	for i, l := range archive.Layers {
		r := results[i]
		obs := make([]bitrate.Observation, 0, len(r.Segments))
		for _, s := range r.Segments {
			obs = append(obs, bitrate.Observation{StartMs: s.StartMs, EndMs: s.EndMs, Bytes: s.ByteEnd - s.ByteStart})
		}
		br := bitrate.Measure(l.TargetKbps, l.DurationMs, cfg.WindowMs, obs)
		layerInputs = append(layerInputs, manifest.LayerInput{
			Result: r, Codec: analyzers[i].codecName, Units: analyzers[i].unitCount,
			Keyframes:  analyzers[i].keyframes,
			TargetKbps: l.TargetKbps, ActualKbps: br.ActualKbps, ErrorPct: br.ErrorPct,
		})
	}

	doc, err := manifest.Build(manifest.Document{
		Version: 1, Case: cfg.Case, Seed: cfg.Seed, ChunkBytes: cfg.ChunkBytes, QueueDepth: cfg.Queue,
		DurationMs: archive.DurationMs, ToleranceMs: cfg.ToleranceMs, Faults: faults,
	}, layerInputs, merge)
	if err != nil {
		return Output{}, err
	}

	vr := verify.Verify(results, merge, doc)
	out := Output{Manifest: doc, Verify: vr, PeakBuffered: peak, QueueBound: bound, Drained: true}
	out.Damaged = damaged(analyzers, results, merge)
	return out, nil
}

// damaged distinguishes structural damage from informational observations:
// split start codes are reassembled correctly and do not make a run damaged.
func damaged(analyzers []*analyzer, results []recovery.Result, merge recovery.Merge) bool {
	for _, a := range analyzers {
		if a.truncation != nil || a.parseable < a.frames {
			return true
		}
		for _, f := range a.faults {
			if f.Class != media.Informational {
				return true
			}
		}
	}
	for _, r := range results {
		if len(r.Gaps) > 0 {
			return true
		}
		for _, f := range r.Faults {
			if f.Class != media.Informational {
				return true
			}
		}
	}
	return len(merge.Conflicts) > 0
}

// router deterministically interleaves layers one chunk at a time.
type router struct {
	layers     []sample.Layer
	chunk      int
	layerIndex int
	offset     []int
	done       []bool
}

func newRouter(layers []sample.Layer, chunk int) *router {
	return &router{layers: layers, chunk: chunk, offset: make([]int, len(layers)), done: make([]bool, len(layers))}
}

// Next implements stream.Feed. Chunks are copied so the fixture stays
// immutable while consumers may retain their delivered slice.
func (r *router) Next(ctx context.Context) (stream.Item, error) {
	if err := ctx.Err(); err != nil {
		return stream.Item{}, err
	}
	for attempts := 0; attempts < len(r.layers); attempts++ {
		i := r.layerIndex
		r.layerIndex = (r.layerIndex + 1) % len(r.layers)
		if r.done[i] {
			continue
		}
		l := r.layers[i]
		start := r.offset[i]
		if start >= len(l.Stream) {
			r.done[i] = true
			continue
		}
		end := start + r.chunk
		if end > len(l.Stream) {
			end = len(l.Stream)
		}
		r.offset[i] = end
		return stream.Item{Layer: l.Name, Chunk: stream.Chunk{Offset: int64(start), Data: l.Stream[start:end]}}, nil
	}
	return stream.Item{}, io.EOF
}
