// Package timeline merges per-track frames onto one unified microsecond
// timeline. It only consumes frames that belong to journal-committed
// segments and indexes that validated cleanly; partial or uncommitted data
// can never appear in the merged output.
package timeline

import (
	"sort"

	"mediaroute/internal/clock"
	"mediaroute/internal/codec"
	"mediaroute/internal/transport"
)

// Frame is one timeline entry after clock-domain conversion.
type Frame struct {
	Track     uint32
	Codec     codec.Codec
	Sample    uint32
	Usec      int64 // unified microsecond timestamp
	RTPTS     uint32 // original RTP timestamp (for evidence)
	Keyframe  bool
	Complete  bool
	SegmentID string // committed segment this frame belongs to
	Bytes     []byte
}

// Timeline is the merged, time-ordered multi-track result.
type Timeline struct {
	Frames []*Frame
}

// TrackClock binds an SSRC to its RTP rate and unwrapped timestamp state.
type TrackClock struct {
	SSRC    uint32
	Codec   codec.Codec
	Rate    clock.Rate
	started bool
	prevTS  int64
}

// NewTrackClock builds a track clock for the standard 90kHz video domain.
func NewTrackClock(ssrc uint32, c codec.Codec) *TrackClock {
	return &TrackClock{SSRC: ssrc, Codec: c, Rate: clock.RTPVideo}
}

// Map converts one raw RTP timestamp into unified microseconds, handling
// non-zero origins and 32-bit wraparound.
func (tc *TrackClock) Map(raw uint32) (int64, error) {
	var unwrapped int64
	if !tc.started {
		tc.started = true
		unwrapped = clock.Begin32(raw)
	} else {
		u, err := clock.Unwrap32(tc.prevTS, raw)
		if err != nil {
			return 0, err
		}
		unwrapped = u
	}
	tc.prevTS = unwrapped
	usec, err := clock.Convert(unwrapped, tc.Rate, clock.Usec)
	if err != nil {
		return 0, clock.E(clock.PhaseTimeline, tc.SSRC, -1, false,
			"track timestamp could not be mapped to unified clock", err)
	}
	return usec, nil
}

// Input groups one track's reassembled frames with the segment that owns
// them. Frames must already be ordered by arrival.
type Input struct {
	Track     *TrackClock
	Frames    []*transport.Frame
	Keyframe  func(frame *transport.Frame) (bool, error)
	SegmentID string
	Codec     codec.Codec
}

// Merge combines all track inputs into one ordered timeline. Only complete
// frames are eligible for merge; truncated frames are retained as excluded
// evidence rather than dropped silently.
func Merge(inputs []*Input) (*Timeline, []*Frame, error) {
	tl := &Timeline{}
	var excluded []*Frame
	for _, in := range inputs {
		for _, fr := range in.Frames {
			usec, err := in.Track.Map(fr.Timestamp)
			if err != nil {
				return nil, nil, err
			}
			key := false
			if in.Keyframe != nil && fr.Complete {
				key, err = in.Keyframe(fr)
				if err != nil {
					return nil, nil, err
				}
			}
			entry := &Frame{
				Track:     in.Track.SSRC,
				Codec:     in.Codec,
				Sample:    0, // filled by caller when sample numbering exists
				Usec:      usec,
				RTPTS:     fr.Timestamp,
				Keyframe:  key,
				Complete:  fr.Complete,
				SegmentID: in.SegmentID,
				Bytes:     append([]byte(nil), fr.Bytes...),
			}
			if !fr.Complete {
				excluded = append(excluded, entry)
				continue
			}
			tl.Frames = append(tl.Frames, entry)
		}
	}
	sort.SliceStable(tl.Frames, func(i, j int) bool {
		if tl.Frames[i].Usec != tl.Frames[j].Usec {
			return tl.Frames[i].Usec < tl.Frames[j].Usec
		}
		return tl.Frames[i].Track < tl.Frames[j].Track
	})
	return tl, excluded, nil
}

// ConservationError reports a failed packet-count conservation check.
type ConservationError struct {
	Stage    string
	Expected int
	Actual   int
}

func (e *ConservationError) Error() string {
	return "packet conservation violated at " + e.Stage +
		": expected=" + itoa(e.Expected) + " actual=" + itoa(e.Actual)
}

// CheckConservation enforces that no packets were invented or silently
// swallowed between stages: distinct accepted + duplicates + lost must equal
// the number of packets the source actually transmitted.
func CheckConservation(stage string, accepted, duplicates, lost, truth int) error {
	actual := accepted + duplicates + lost
	if actual != truth {
		return &ConservationError{Stage: stage, Expected: truth, Actual: actual}
	}
	return nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
