// Package clock provides the project's single media-time representation.
//
// All containers and RTP timestamp scales are converted to integer
// nanoseconds at the boundary. Arrival time and simulated bandwidth pacing
// never alter these values.
package clock

import (
	"fmt"
	"time"

	"mediacore/internal/fault"
)

// NSPerSecond is the unified clock tick rate.
const NSPerSecond int64 = int64(time.Second)

// Timebase converts between container/RTP units and nanoseconds.
type Timebase struct {
	Timescale uint32 `json:"timescale"`
}

// ToNS converts signed timebase units to nanoseconds.
func (tb Timebase) ToNS(units int64) int64 {
	if tb.Timescale == 0 {
		return 0
	}
	// Round to nearest to keep sub-millisecond alignment stable.
	if units >= 0 {
		return (units*NSPerSecond + int64(tb.Timescale)/2) / int64(tb.Timescale)
	}
	return -((-units*NSPerSecond + int64(tb.Timescale)/2) / int64(tb.Timescale))
}

// FromNS converts nanoseconds to timebase units, rounding down for DTS.
func (tb Timebase) FromNS(ns int64) int64 {
	if tb.Timescale == 0 {
		return 0
	}
	return ns * int64(tb.Timescale) / NSPerSecond
}

// DurationMS converts nanoseconds to floating-point milliseconds.
func DurationMS(ns int64) float64 { return float64(ns) / 1e6 }

// AlignmentMS returns the signed millisecond difference a-b.
func AlignmentMS(aNS, bNS int64) float64 { return float64(aNS-bNS) / 1e6 }

// Point is one committed sample timestamp on a track.
type Point struct {
	SampleIndex uint32 `json:"sample_index"`
	PTSNS       int64  `json:"pts_ns"`
	DTSNS       int64  `json:"dts_ns"`
}

// Gap records media time for packets/samples that were not recovered.
type Gap struct {
	TrackID      uint32 `json:"track_id"`
	SSRC         uint32 `json:"ssrc,omitempty"`
	StartSeq     uint32 `json:"start_seq"`
	EndSeq       uint32 `json:"end_seq"`
	MissingCount int    `json:"missing_count"`
	StartPTSNS   int64  `json:"start_pts_ns,omitempty"`
	EndPTSNS     int64  `json:"end_pts_ns,omitempty"`
	Reason       string `json:"reason"`
}

type trackState struct {
	last       Point
	committed  bool
	committedN uint64
}

// Timeline guards committed per-track monotonicity. A late point is rejected
// rather than inserted before an already committed point.
type Timeline struct {
	tracks map[uint32]*trackState
	Gaps   []Gap `json:"gaps"`
}

// NewTimeline creates an empty unified timeline.
func NewTimeline() *Timeline {
	return &Timeline{tracks: make(map[uint32]*trackState)}
}

// Commit accepts a point if it is not earlier than the committed DTS.
func (t *Timeline) Commit(trackID uint32, p Point) (accepted bool, err error) {
	st := t.tracks[trackID]
	if st == nil {
		st = &trackState{}
		t.tracks[trackID] = st
	}
	if st.committed && p.DTSNS < st.last.DTSNS {
		return false, fault.New(fault.Conflict, fmt.Sprintf(
			"late sample track=%d index=%d dts_ns=%d below committed_dts_ns=%d",
			trackID, p.SampleIndex, p.DTSNS, st.last.DTSNS))
	}
	st.last = p
	st.committed = true
	st.committedN++
	return true, nil
}

// Last returns the most recently committed point.
func (t *Timeline) Last(trackID uint32) (Point, bool) {
	st := t.tracks[trackID]
	if st == nil || !st.committed {
		return Point{}, false
	}
	return st.last, true
}

// CommittedCount returns the number of commits for a track.
func (t *Timeline) CommittedCount(trackID uint32) uint64 {
	if st := t.tracks[trackID]; st != nil {
		return st.committedN
	}
	return 0
}

// AddGap records a definite loss interval.
func (t *Timeline) AddGap(g Gap) { t.Gaps = append(t.Gaps, g) }

// BandwidthPacer models variable bandwidth without sleeping in tests. Bytes
// are charged against the current rate; callers may use WaitNS to simulate
// streaming read rhythm.
type BandwidthPacer struct {
	bytesPerSecond int64
	creditNS       int64
}

// NewPacer creates a pacer. Non-positive rate means unbounded.
func NewPacer(bitsPerSecond int64) *BandwidthPacer {
	if bitsPerSecond <= 0 {
		return &BandwidthPacer{}
	}
	return &BandwidthPacer{bytesPerSecond: bitsPerSecond / 8}
}

// Charge accounts for n bytes and returns the virtual read duration.
func (p *BandwidthPacer) Charge(n int) time.Duration {
	if p == nil || p.bytesPerSecond <= 0 || n <= 0 {
		return 0
	}
	ns := int64(n) * NSPerSecond / p.bytesPerSecond
	p.creditNS += ns
	d := p.creditNS
	p.creditNS = 0
	return time.Duration(d)
}
