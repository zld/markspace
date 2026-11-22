package timeline

import (
	"fmt"

	"archivist/internal/model"
)

// Boundary is one aligned cut point expressed in the common media clock.
type Boundary struct {
	Index       int
	CommonTicks uint64
	Seconds     model.Rat
	// PerTrackTicks records each participating track's exact DTS at this
	// boundary, keyed by track id.
	PerTrackTicks map[uint32]uint64
}

// BoundaryDrift quantifies a failed alignment between two representations.
type BoundaryDrift struct {
	TrackID        uint32
	PeerTrackID    uint32
	CommonTicks    uint64
	NearestTicks   uint64
	DeviationTicks uint64
	Seconds        model.Rat
}

func (d BoundaryDrift) String() string {
	return fmt.Sprintf("keyframe-index:%s drift=%d ticks (%s)",
		trackLabel(d.TrackID), d.DeviationTicks, d.Seconds)
}

// trackLabel is injected by the caller layer (segmenter) so timeline stays
// free of naming policy. Defaults to the numeric id.
var trackLabel = func(id uint32) string { return fmt.Sprintf("track-%d", id) }

// SetTrackLabeler installs a function mapping track id to a report label.
func SetTrackLabeler(fn func(uint32) string) {
	if fn != nil {
		trackLabel = fn
	}
}

// TrackClock pairs a parsed track with its validated Clock.
type TrackClock struct {
	Track     *model.Track
	Clock     Clock
	KeyDTS    []uint64 // keyframe DTS in this track's timescale
}

// AlignmentResult is the outcome of multi-bitrate boundary alignment.
type AlignmentResult struct {
	CommonTimescale uint64
	Boundaries      []Boundary
	Drifts          []BoundaryDrift
	Aligned         bool
}

// AlignBoundaries computes cut boundaries that are keyframes in EVERY video
// representation simultaneously, expressed on a common LCM timescale. When no
// common keyframe exists at a required position it records the deviation and
// marks the result unaligned — it never moves a boundary to force a match.
func AlignBoundaries(tracks []TrackClock, targetSegments int) (*AlignmentResult, error) {
	if len(tracks) < 2 {
		return nil, model.AtTrack(model.StageTimeline, model.ErrNoCommonKeyframe, 0,
			"alignment needs at least two representations")
	}
	scales := make([]uint32, len(tracks))
	for i, tc := range tracks {
		scales[i] = tc.Clock.Timescale()
	}
	common, err := CommonTimescale(scales)
	if err != nil {
		return nil, err
	}
	res := &AlignmentResult{CommonTimescale: common}

	// Reference keyframe grid = the first video track's keyframes mapped onto
	// the common clock. Every other representation must contain a keyframe at
	// each reference point.
	ref := tracks[0]
	var refCommon []uint64
	for _, dts := range ref.KeyDTS {
		mapped, err := ref.Clock.Convert(dts, Clock{timescale: uint32(common)})
		if err != nil {
			return nil, err
		}
		refCommon = append(refCommon, mapped)
	}
	keySets := make([]map[uint64]bool, len(tracks))
	for i, tc := range tracks {
		keySets[i] = map[uint64]bool{}
		for _, dts := range tc.KeyDTS {
			m, err := tc.Clock.Convert(dts, Clock{timescale: uint32(common)})
			if err != nil {
				return nil, err
			}
			keySets[i][m] = true
		}
	}

	alignedCount := 0
	for idx, point := range refCommon {
		allMatch := true
		for ti := 1; ti < len(tracks); ti++ {
			if !keySets[ti][point] {
				allMatch = false
				nearest := nearestKey(keySets[ti], point)
				dev := subtractAbs(point, nearest)
				secs, _ := model.NewRat(int64(dev), common)
				res.Drifts = append(res.Drifts, BoundaryDrift{
					TrackID:        tracks[ti].Track.ID,
					PeerTrackID:    ref.Track.ID,
					CommonTicks:    point,
					NearestTicks:   nearest,
					DeviationTicks: dev,
					Seconds:        secs,
				})
			}
		}
		if allMatch {
			b := Boundary{
				Index:         idx,
				CommonTicks:   point,
				PerTrackTicks: map[uint32]uint64{},
			}
			for ti, tc := range tracks {
				b.PerTrackTicks[tc.Track.ID] = invertCommon(common, point, tc.Clock.Timescale(), keySets[ti])
			}
			b.Seconds, _ = model.NewRat(int64(point), common)
			res.Boundaries = append(res.Boundaries, b)
			alignedCount++
		}
	}
	res.Aligned = len(res.Drifts) == 0 && alignedCount >= targetSegments
	return res, nil
}

// nearestKey returns the keyframe common-clock tick closest to target.
func nearestKey(set map[uint64]bool, target uint64) uint64 {
	var best uint64
	bestDiff := uint64(1<<63 - 1)
	for k := range set {
		d := subtractAbs(k, target)
		if d < bestDiff {
			bestDiff, best = d, k
		}
	}
	return best
}

func subtractAbs(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

// invertCommon finds the native-track tick corresponding to a common-clock
// keyframe point. The point is known to exist in keySet on this clock.
func invertCommon(common, point uint64, native uint32, keySet map[uint64]bool) uint64 {
	if keySet[point] {
		// exact common point exists; convert back to native ticks
		return point * uint64(native) / common
	}
	return 0
}

// RequiredSegmentCount derives how many aligned boundaries a manifest needs
// from the desired segment duration and total common-clock length.
func RequiredSegmentCount(totalCommonTicks, commonTS uint64, segmentMS uint32) int {
	segTicks := uint64(segmentMS) * commonTS / 1000
	if segTicks == 0 {
		return 0
	}
	return int(totalCommonTicks / segTicks)
}
