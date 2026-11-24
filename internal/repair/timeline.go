// Package repair maps reordered RTP frames onto a single monotonic media
// time base, isolates truncated or anomalous samples and normalizes variable
// frame-rate gaps without rewriting committed boundaries.
package repair

import (
	"mediasync/internal/clock"
	"mediasync/internal/rtp"
)

// Sample is one recovered media sample on the unified media time line.
type Sample struct {
	Index        int    // position in the recovered timeline
	Seq          uint16 // source RTP sequence number
	IsKeyframe   bool
	IsRandomAccess bool
	MediaNS      int64 // monotonic media time in nanoseconds
	ExtendedRTP  int64 // unwrapped RTP timestamp
	SizeBytes    int
	Synthetic    bool // true when the sample fills a committed loss gap
	Isolated     bool // true when truncated/anomalous and kept out of the index
}

// GapKind classifies a discontinuity detected on the timeline.
type GapKind int

const (
	GapNone GapKind = iota
	GapLost
	GapTruncated
	GapBackwards
	GapNonMonotonicRTP
)

// TimelineCounters summarizes recovery decisions for the metrics report.
type TimelineCounters struct {
	SamplesIn        uint64
	SamplesOut       uint64
	SyntheticFills   uint64
	Isolated         uint64
	RejectedBoundary uint64
	GapsLost         uint64
	GapsTruncated    uint64
	Backwards        uint64
	FirstFailSeq     uint16
	FirstFailKind    clock.ErrorKind
	haveFail         bool
}

// TimelineBuilder performs per-representation recovery. It owns a drift-aware
// MediaClock and never mutates a boundary once samples have been emitted.
type TimelineBuilder struct {
	ssrc      uint32
	rate      uint32
	clock     *clock.MediaClock
	anchorSet bool
	prevRTP   int64
	prevMedia int64
	nextIndex int
	samples   []Sample
	c         TimelineCounters
	maxBytes  int
}

// NewTimelineBuilder constructs a recovery builder for one representation.
func NewTimelineBuilder(ssrc uint32, rate uint32, maxSampleBytes int) *TimelineBuilder {
	if maxSampleBytes <= 0 || maxSampleBytes > clock.MaxPacketBytes {
		maxSampleBytes = clock.MaxPacketBytes
	}
	return &TimelineBuilder{
		ssrc:     ssrc,
		rate:     rate,
		clock:    clock.NewMediaClock(rate),
		maxBytes: maxSampleBytes,
	}
}

// FitClock calibrates drift using RTCP-derived sender reports.
func (b *TimelineBuilder) FitClock(est *clock.DriftEstimator) bool {
	return b.clock.Fit(est)
}

// AddFrame turns one ordered RTP frame into a recovered sample. A sample whose
// RTP timestamp regresses, whose payload exceeds the size bound, or that is
// flagged truncated at the source is isolated rather than indexed.
func (b *TimelineBuilder) AddFrame(f rtp.Frame, keyframe, truncated bool) (*Sample, *clock.Error) {
	b.c.SamplesIn++
	ext := f.ExtendedTS
	if b.anchorSet {
		if ext <= b.prevRTP {
			return nil, b.isolate(f, GapBackwards, clock.ErrBackwards, "non-monotonic rtp timestamp")
		}
	} else {
		b.clock.Anchor(ext)
		b.anchorSet = true
		b.prevRTP = ext
	}
	if truncated || len(f.Packet.Payload) == 0 || len(f.Packet.Payload) > b.maxBytes {
		return nil, b.isolate(f, GapTruncated, clock.ErrTruncated, "sample truncated or size out of bounds")
	}
	mediaNS, err := b.clock.ToMediaNS(ext)
	if err != nil {
		return nil, b.isolate(f, GapNonMonotonicRTP, err.Kind, err.Message)
	}
	if mediaNS < b.prevMedia && b.nextIndex > 0 {
		return nil, b.isolate(f, GapBackwards, clock.ErrBackwards, "media time regressed")
	}
	s := Sample{
		Index:          b.nextIndex,
		Seq:            f.Packet.SequenceNumber,
		IsKeyframe:     keyframe,
		IsRandomAccess: keyframe,
		MediaNS:        mediaNS,
		ExtendedRTP:    ext,
		SizeBytes:      len(f.Packet.Payload),
	}
	b.nextIndex++
	b.prevRTP = ext
	b.prevMedia = mediaNS
	b.samples = append(b.samples, s)
	b.c.SamplesOut++
	return &s, nil
}

// FillGap inserts a synthetic placeholder for a committed loss between the
// previous sample and the next expected RTP timestamp. It keeps the timeline
// contiguous so downstream indexing never observes a hole.
func (b *TimelineBuilder) FillGap(seq uint16, expectedRTP int64) Sample {
	mediaNS, err := b.clock.ToMediaNS(expectedRTP)
	if err != nil {
		mediaNS = b.prevMedia
	}
	s := Sample{
		Index:       b.nextIndex,
		Seq:         seq,
		MediaNS:     mediaNS,
		ExtendedRTP: expectedRTP,
		Synthetic:   true,
		Isolated:    true,
	}
	b.nextIndex++
	b.samples = append(b.samples, s)
	b.c.SyntheticFills++
	b.c.GapsLost++
	return s
}

// isolate records a rejected sample and the first failing packet.
func (b *TimelineBuilder) isolate(f rtp.Frame, gap GapKind, kind clock.ErrorKind, msg string) *clock.Error {
	ce := clock.NewError(kind, msg)
	ce.Sequence = f.Packet.SequenceNumber
	ce.SSRC = f.Packet.SSRC
	if !b.c.haveFail {
		b.c.FirstFailSeq = f.Packet.SequenceNumber
		b.c.FirstFailKind = kind
		b.c.haveFail = true
	}
	switch gap {
	case GapBackwards:
		b.c.Backwards++
	case GapTruncated:
		b.c.GapsTruncated++
	}
	b.c.Isolated++
	b.c.RejectedBoundary++
	return ce
}

// Samples returns the recovered, indexable samples in media order.
func (b *TimelineBuilder) Samples() []Sample { return b.samples }

// Counters returns recovery counters.
func (b *TimelineBuilder) Counters() TimelineCounters { return b.c }

// DriftPPM exposes the calibrated drift in parts per million.
func (b *TimelineBuilder) DriftPPM() float64 { return b.clock.DriftPPM() }
