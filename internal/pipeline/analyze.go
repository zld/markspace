package pipeline

import (
	"context"
	"fmt"
	"sort"
	"time"

	"archivecheck/internal/codec"
	"archivecheck/internal/media"
	"archivecheck/internal/recovery"
	"archivecheck/internal/stream"
)

// slowLatencyBudgetMs bounds the total artificial consumer latency per layer.
// Sleeping on every chunk at the acceptance chunk size would take hours, so
// the budget is spread over an evenly spaced sample of chunks. This still
// forces genuine queue pressure and producer stalls.
const slowLatencyBudgetMs = 600

// analyzer is a fan-out consumer that parses its layer incrementally. It keeps
// only fixed parser state plus one record per frame-bearing unit, so its
// memory does not scale with the number of chunks or the source length.
type analyzer struct {
	name      string
	codecName string
	clock     media.Clock

	nal *codec.NALParser
	av1 *codec.AV1Parser

	delay   time.Duration
	every   int
	seen    int
	delayed int

	units      []recovery.Unit
	boundaries []int64

	unitCount int
	keyframes int
	frames    int
	parseable int

	faults     []media.Fault
	truncation *recovery.Truncation
	feedErr    error
}

// newAnalyzer builds a fixed-state consumer for one layer.
func newAnalyzer(name string, c codec.Codec, clock media.Clock, delayMs, chunkBytes, streamLen int) (*analyzer, error) {
	a := &analyzer{name: name, codecName: string(c), clock: clock, delay: time.Duration(delayMs) * time.Millisecond}
	switch c {
	case codec.H264, codec.H265:
		p, err := codec.NewNALParser(c)
		if err != nil {
			return nil, err
		}
		a.nal = p
	case codec.AV1:
		a.av1 = codec.NewAV1Parser()
	default:
		return nil, fmt.Errorf("pipeline: unsupported codec %q", c)
	}
	if delayMs > 0 && chunkBytes > 0 && streamLen > 0 {
		chunks := (streamLen + chunkBytes - 1) / chunkBytes
		samples := slowLatencyBudgetMs / delayMs
		if samples < 1 {
			samples = 1
		}
		if samples > chunks {
			samples = chunks
		}
		a.every = chunks / samples
		if a.every < 1 {
			a.every = 1
		}
	}
	return a, nil
}

// Name implements stream.Consumer.
func (a *analyzer) Name() string { return a.name }

// Offer implements stream.Consumer and parses the delivered chunk in place.
func (a *analyzer) Offer(ctx context.Context, ch stream.Chunk) error {
	if a.delay > 0 && a.every > 0 && a.seen%a.every == 0 {
		a.delayed++
		t := time.NewTimer(a.delay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
	a.seen++
	if a.nal != nil {
		for _, u := range a.nal.Feed(ch.Data) {
			a.recordNAL(u)
		}
		return nil
	}
	for _, o := range a.av1.Feed(ch.Data) {
		a.recordOBU(o)
	}
	return nil
}

// Close implements stream.Consumer and finalizes cumulative parser state.
func (a *analyzer) Close() error {
	if a.nal != nil {
		for _, u := range a.nal.Flush() {
			a.recordNAL(u)
		}
		rep := a.nal.Report()
		a.unitCount = rep.Units
		a.keyframes = rep.Keyframes
		a.faults = append(a.faults, a.nal.Faults(a.name)...)
		if rep.Truncated > 0 {
			a.truncation = &recovery.Truncation{Offset: rep.FirstDamage, Kind: media.KindNALTruncated,
				Detail: fmt.Sprintf("%d NAL units were cut by end of stream", rep.Truncated)}
		}
	} else {
		for _, o := range a.av1.Flush() {
			a.recordOBU(o)
		}
		rep := a.av1.Report()
		a.unitCount = rep.Units
		a.keyframes = rep.Keyframes
		a.faults = append(a.faults, a.av1.Faults(a.name)...)
		if rep.Truncated > 0 {
			a.truncation = &recovery.Truncation{Offset: rep.FirstDamage, Kind: media.KindOBUTruncated,
				Detail: fmt.Sprintf("%d OBUs were cut by end of stream", rep.Truncated)}
		}
	}
	sort.SliceStable(a.boundaries, func(i, j int) bool { return a.boundaries[i] < a.boundaries[j] })
	return nil
}

func (a *analyzer) recordNAL(u codec.NALUnit) {
	if !u.Frame {
		return
	}
	a.frames++
	if !u.Truncated && u.Length > 0 {
		a.parseable++
		if u.Keyframe {
			a.boundaries = append(a.boundaries, u.Offset)
		}
	}
	a.units = append(a.units, recovery.Unit{Offset: u.Offset, End: u.End(), Keyframe: u.Keyframe, Truncated: u.Truncated})
}

func (a *analyzer) recordOBU(o codec.OBU) {
	if !o.Frame {
		return
	}
	a.frames++
	if !o.Truncated {
		a.parseable++
		if o.Keyframe {
			a.boundaries = append(a.boundaries, o.Offset)
		}
	}
	a.units = append(a.units, recovery.Unit{Offset: o.Offset, End: o.End(), Keyframe: o.Keyframe, Truncated: o.Truncated})
}
