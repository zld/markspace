package timeline

import (
	"fmt"

	"github.com/pion/rtp"
)

// DefaultJumpThresholdTicks is the default backwards/forwards excursion (in
// raw clock ticks) that is treated as a source-switch boundary rather than
// ordinary jitter. A normal packet delta stays well below it; a clock reset
// or RTP source change lands beyond it.
const DefaultJumpThresholdTicks = uint64(5_000_000)

// ExpandTimestamp unwraps a 32-bit RTP timestamp relative to the previously
// expanded value. The signed low-32-bit delta treats excursions within half
// the 32-bit space as linear progress, so a wrap at 2^32 keeps the expanded
// sequence strictly monotonic instead of jumping backwards.
func ExpandTimestamp(prevExpanded uint64, raw uint32) uint64 {
	delta := int32(raw - uint32(prevExpanded))
	return uint64(int64(prevExpanded) + int64(delta))
}

// ExpandSequence performs the same unwrap for 16-bit RTP sequence numbers.
func ExpandSequence(prevExpanded uint64, raw uint16) uint64 {
	delta := int16(raw - uint16(prevExpanded))
	return uint64(int64(prevExpanded) + int64(delta))
}

// TrackNormalizer linearizes one track's raw clock into exact Rational
// timestamps. It preserves the originating ticks, clock reference, wrap
// count and input position; VFR material is accumulated sample by sample and
// never projected through an average frame rate.
type TrackNormalizer struct {
	track     TrackID
	clock     ClockRef
	timescale uint32

	jumpThreshold uint64

	lastExpanded uint64
	lastSeq      uint64
	lastDTS      Rational
	started      bool
	failed       bool
	failReason   string

	order uint64
	wraps uint32
	errs  []*StageError
}

// NewTrackNormalizer creates a normalizer for one track. timescale is the
// MP4 track timescale or the RTP clock rate; zero is rejected on first use.
func NewTrackNormalizer(track TrackID, clock ClockRef, timescale uint32, jumpThresholdTicks uint64) *TrackNormalizer {
	if jumpThresholdTicks == 0 {
		jumpThresholdTicks = DefaultJumpThresholdTicks
	}
	return &TrackNormalizer{
		track:         track,
		clock:         clock,
		timescale:     timescale,
		jumpThreshold: jumpThresholdTicks,
	}
}

// Failed reports whether the track has been marked unrecoverable.
func (n *TrackNormalizer) Failed() bool { return n.failed }

// FailReason returns the failure detail once Failed is true.
func (n *TrackNormalizer) FailReason() string { return n.failReason }

// Errors returns the non-fatal per-track errors observed so far.
func (n *TrackNormalizer) Errors() []*StageError { return n.errs }

// Wraps returns the number of 32-bit wraps unwrapped on this track.
func (n *TrackNormalizer) Wraps() uint32 { return n.wraps }

// Fail marks the track unrecoverable with the supplied reason. Subsequent
// pushes return ErrTrackFailed instead of silently dropping data.
func (n *TrackNormalizer) Fail(reason string) {
	n.failed = true
	n.failReason = reason
	n.errs = append(n.errs, &StageError{
		Stage:       StageNormalize,
		Kind:        KindUnrecoverable,
		Track:       n.track,
		Recoverable: false,
		Detail:      reason,
		Err:         ErrTrackFailed,
	})
}

// PushRTP linearizes one parsed RTP packet. A boundary jump is returned as a
// StageError without advancing internal state; the repair layer must then
// call ApplyCorrection or Fail. Duplicate timestamps are recorded as
// recoverable errors and yield a nil timestamp.
func (n *TrackNormalizer) PushRTP(p *rtp.Packet) (*Timestamp, error) {
	if n.failed {
		return nil, n.trackErr(KindUnrecoverable, -1, 0, ErrTrackFailed, n.failReason)
	}
	if n.timescale == 0 {
		return nil, n.trackErr(KindMissingTrack, -1, n.order, nil, "RTP clock rate is zero")
	}
	n.order++
	if n.started {
		newSeq := ExpandSequence(n.lastSeq, p.SequenceNumber)
		if newSeq <= n.lastSeq {
			n.record(KindDuplicateTime, -1, n.order, true,
				fmt.Sprintf("sequence %d is not after %d", p.SequenceNumber, uint16(n.lastSeq)))
		}
	}
	expanded := uint64(p.Timestamp)
	if n.started {
		prevRaw := uint32(n.lastExpanded)
		expanded = ExpandTimestamp(n.lastExpanded, p.Timestamp)
		// A 32-bit wrap shows up as a smaller raw value while the
		// expanded clock still moves forward.
		if p.Timestamp < prevRaw && expanded > n.lastExpanded {
			n.wraps++
		}
	}
	delta := int64(expanded) - int64(n.lastExpanded)
	if n.started && abs64(delta) > int64(n.jumpThreshold) {
		return nil, n.trackErr(KindBoundaryJump, -1, n.order, nil,
			fmt.Sprintf("raw %d vs previous raw %d (delta ticks %d)", p.Timestamp, uint32(n.lastExpanded), delta))
	}
	ts := n.makeTimestamp(int64(expanded), int64(expanded), p.Marker)
	ts.WrapCount = n.wraps
	if n.started && ts.DTS.Cmp(n.lastDTS) == 0 {
		n.record(KindDuplicateTime, -1, n.order, true,
			fmt.Sprintf("expanded timestamp %d repeats previous", expanded))
		return nil, nil
	}
	n.advance(expanded, uint64(p.SequenceNumber), ts.DTS)
	return &ts, nil
}

// PushSample linearizes one MP4-style sample whose DTS/PTS are already raw
// ticks on the track timescale.
func (n *TrackNormalizer) PushSample(rawDTS, rawPTS int64) (*Timestamp, error) {
	if n.failed {
		return nil, n.trackErr(KindUnrecoverable, -1, n.order, ErrTrackFailed, n.failReason)
	}
	if n.timescale == 0 {
		return nil, n.trackErr(KindMissingTrack, -1, n.order, nil, "track timescale is zero")
	}
	n.order++
	if n.started && rawDTS <= n.lastExpandedAsRaw() {
		n.record(KindDuplicateTime, -1, n.order, true,
			fmt.Sprintf("raw DTS %d is not after %d", rawDTS, n.lastExpandedAsRaw()))
		return nil, nil
	}
	ts := n.makeTimestamp(rawDTS, rawPTS, false)
	n.advance(uint64(rawDTS), 0, ts.DTS)
	return &ts, nil
}

// ApplyCorrection accepts a boundary-jump position, applies a signed tick
// offset and returns the corrected monotonic timestamp. The raw ticks and
// the offset are both retained on the timestamp's origin for traceability.
func (n *TrackNormalizer) ApplyCorrection(rawDTS int64, offsetTicks int64, position uint64) *Timestamp {
	corrected := rawDTS + offsetTicks
	ts := n.makeTimestampAt(corrected, corrected, position, true)
	n.advance(uint64(corrected), 0, ts.DTS)
	return &ts
}

// LastExpanded returns the most recent expanded tick value.
func (n *TrackNormalizer) LastExpanded() uint64 { return n.lastExpanded }

func (n *TrackNormalizer) makeTimestamp(rawDTS, rawPTS int64, marker bool) Timestamp {
	return n.makeTimestampAt(rawDTS, rawPTS, n.order, marker)
}

func (n *TrackNormalizer) makeTimestampAt(rawDTS, rawPTS int64, order uint64, marker bool) Timestamp {
	return Timestamp{
		Track:     n.track,
		Clock:     n.clock,
		RawDTS:    rawDTS,
		RawPTS:    rawPTS,
		Timescale: n.timescale,
		DTS:       FromTicks(rawDTS, n.timescale),
		PTS:       FromTicks(rawPTS, n.timescale),
		WrapCount: n.wraps,
		Order:     order,
	}
}

func (n *TrackNormalizer) advance(expanded, seq uint64, dts Rational) {
	n.lastExpanded = expanded
	n.lastSeq = seq
	n.lastDTS = dts
	n.started = true
}

func (n *TrackNormalizer) lastExpandedAsRaw() int64 { return int64(n.lastExpanded) }

func (n *TrackNormalizer) record(kind string, offset int64, pos uint64, recoverable bool, detail string) {
	n.errs = append(n.errs, n.trackErr(kind, offset, pos, nil, detail))
}

func (n *TrackNormalizer) trackErr(kind string, offset int64, pos uint64, cause error, detail string) *StageError {
	return &StageError{
		Stage:       StageNormalize,
		Kind:        kind,
		Offset:      offset,
		Position:    pos,
		Track:       n.track,
		Recoverable: kind != KindUnrecoverable,
		Detail:      detail,
		Err:         cause,
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
