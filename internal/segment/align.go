// Package segment aligns representations on shared media-time boundaries.
//
// Boundaries are a fixed grid anchored at the shared epoch, so every
// representation that can serve a boundary produces the same start/end pair.
// A boundary not covered by a trusted keyframe within the configured search
// budget is reported as unalignable. Untrusted intervals from RTCP regressions
// therefore surface here rather than being smoothed over.
package segment

import (
	"fmt"
	"sort"

	"mediatool/internal/fault"
	"mediatool/internal/index"
)

// Config controls alignment.
type Config struct {
	// DurationMs is the nominal segment duration. Zero means 1000.
	DurationMs int64
	// KeyframeSearchMs is how far past a boundary a trusted keyframe may be
	// found. Zero means the segment duration.
	KeyframeSearchMs int64
}

func (c Config) duration() int64 {
	if c.DurationMs <= 0 {
		return 1000
	}
	return c.DurationMs
}

func (c Config) search() int64 {
	if c.KeyframeSearchMs <= 0 {
		return c.duration()
	}
	return c.KeyframeSearchMs
}

// Boundary is one slot on the shared grid.
type Boundary struct {
	Index   int   `json:"index"`
	StartMs int64 `json:"start_ms"`
	EndMs   int64 `json:"end_ms"`
}

// Representation is one track's aligned view of one boundary.
type Representation struct {
	RepresentationID string `json:"representation_id"`
	TrackID          uint32 `json:"track_id"`
	KeyframeOffset   int64  `json:"keyframe_offset"`
	KeyframeSeq      uint16 `json:"keyframe_seq"`
	SkewMs           int64  `json:"skew_ms"`
	Aligned          bool   `json:"aligned"`
}

// Segment is one aligned output boundary across all representations.
type Segment struct {
	Boundary
	Representations []Representation `json:"representations"`
}

// Unaligned explains why a boundary could not be covered.
type Unaligned struct {
	Boundary
	CoverageID string `json:"coverage_id"`
	Detail     string `json:"detail"`
}

// Result is a full alignment pass.
type Result struct {
	Segments  []Segment   `json:"segments"`
	MaxSkewMs int64       `json:"max_skew_ms"`
	Unaligned []Unaligned `json:"unaligned,omitempty"`
	Faults    []*fault.Fault
}

// Coverage is one track's keyframe timeline.
type Coverage struct {
	ID    string
	Track *index.Track
	// Trusted reports whether a media time is on a trusted clock. nil means
	// every time is trusted.
	Trusted func(mediaMs int64) bool
}

// Align builds the shared grid over the intersection of coverage time ranges
// and maps each coverage's trusted keyframes onto it.
func Align(covs []Coverage, cfg Config) (*Result, error) {
	if len(covs) == 0 {
		return &Result{}, nil
	}
	dur, search := cfg.duration(), cfg.search()

	start := int64(-1 << 62)
	end := int64(1 << 62)
	for _, c := range covs {
		if c.Track == nil {
			return nil, fmt.Errorf("segment: nil track for coverage %s", c.ID)
		}
		if !c.Track.Time.Valid {
			return nil, (&fault.Fault{Kind: fault.KindAlignment, TrackID: c.Track.TrackID,
				Recovery: fault.RecoverAbort, Severity: fault.SeverityFatal}).
				WithDetail("track %d has no trusted time range; cannot align", c.Track.TrackID)
		}
		if c.Track.Time.StartMs > start {
			start = c.Track.Time.StartMs
		}
		if c.Track.Time.EndMs < end {
			end = c.Track.Time.EndMs
		}
	}
	if end < start {
		return nil, (&fault.Fault{Kind: fault.KindAlignment, Recovery: fault.RecoverAbort,
			Severity: fault.SeverityFatal}).WithDetail("time ranges do not overlap: start=%d end=%d", start, end)
	}

	// Anchor the grid at zero; every run yields identical boundaries.
	first := floorDiv(start, dur) * dur
	if first < start {
		first += dur
	}

	res := &Result{}
	idx := 0
	for b := first; b+search <= end || b < end; b += dur {
		bd := Boundary{Index: idx, StartMs: b, EndMs: b + dur}
		idx++
		seg := Segment{Boundary: bd}
		allAligned := true
		for _, c := range covs {
			kf, ok, detail := pickKeyframe(c, b, search)
			if !ok {
				allAligned = false
				res.Unaligned = append(res.Unaligned, Unaligned{Boundary: bd, CoverageID: c.ID, Detail: detail})
				res.Faults = append(res.Faults, (&fault.Fault{
					Kind: fault.KindAlignment, TrackID: c.Track.TrackID,
					Offset: c.Track.Packets.LastOffset, Recovery: fault.RecoverAbort,
					Severity: fault.SeverityDamaged,
				}).WithDetail("%s", detail))
				seg.Representations = append(seg.Representations, Representation{
					RepresentationID: c.ID, TrackID: c.Track.TrackID, Aligned: false,
				})
				continue
			}
			skew := kf.MediaMs - b
			if skew > res.MaxSkewMs {
				res.MaxSkewMs = skew
			}
			seg.Representations = append(seg.Representations, Representation{
				RepresentationID: c.ID, TrackID: c.Track.TrackID, KeyframeOffset: kf.Offset,
				KeyframeSeq: kf.Seq, SkewMs: skew, Aligned: true,
			})
		}
		if allAligned {
			sort.SliceStable(seg.Representations, func(i, j int) bool {
				if seg.Representations[i].TrackID != seg.Representations[j].TrackID {
					return seg.Representations[i].TrackID < seg.Representations[j].TrackID
				}
				return seg.Representations[i].RepresentationID < seg.Representations[j].RepresentationID
			})
			res.Segments = append(res.Segments, seg)
		}
	}
	if idx == 0 {
		return nil, (&fault.Fault{Kind: fault.KindAlignment, Recovery: fault.RecoverAbort,
			Severity: fault.SeverityFatal}).WithDetail("no segment boundary fits in [%d,%d]", start, end)
	}
	return res, nil
}

// pickKeyframe finds the first trusted keyframe at or after b within budget.
func pickKeyframe(c Coverage, b, budget int64) (index.Keyframe, bool, string) {
	sawUntrusted := false
	for _, k := range c.Track.Keyframes {
		if k.MediaMs < b {
			continue
		}
		if k.MediaMs > b+budget {
			break
		}
		if c.Trusted != nil && !c.Trusted(k.MediaMs) {
			sawUntrusted = true
			continue
		}
		return k, true, ""
	}
	if sawUntrusted {
		return index.Keyframe{}, false, fmt.Sprintf(
			"track %d: keyframes in [%d,%d] lie in an untrusted RTCP interval", c.Track.TrackID, b, b+budget)
	}
	return index.Keyframe{}, false, fmt.Sprintf(
		"track %d: no keyframe in [%d,%d]", c.Track.TrackID, b, b+budget)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}
