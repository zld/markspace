package sync

import (
	"errors"
	"fmt"
)

// TrackKind enumerates the media classes the aligner understands.
type TrackKind uint8

const (
	TrackAudio TrackKind = iota
	TrackVideo
	TrackSubtitle
)

func (k TrackKind) String() string {
	switch k {
	case TrackAudio:
		return "audio"
	case TrackVideo:
		return "video"
	case TrackSubtitle:
		return "subtitle"
	default:
		return "unknown"
	}
}

// Anchor is the first valid timing point of one track on both clocks.
type Anchor struct {
	Kind     TrackKind
	TrackID  string
	MediaNS  int64
	SharedNS int64
}

// Cue is the subtitle representation consumed by the aligner. It mirrors
// timeline.Cue without importing that package, keeping the contract explicit.
type Cue struct {
	Index   int
	StartNS int64
	EndNS   int64
	Text    string
}

// Thresholds bound acceptable inter-track disagreement.
type Thresholds struct {
	MaxOffsetNS      int64 // absolute audio-vs-video anchor offset
	MaxDriftPPM      float64
	MaxSubtitleShift int64 // subtitle anchors may only move within this bound
	// GuardOffsetNS is the A/V delta at which the subtitle guard engages,
	// independent of MaxOffsetNS so the guard can stay strict even when a
	// pipeline deliberately tolerates a wider A/V offset.
	GuardOffsetNS int64
	// SilentSubtitleGuard forbids shifting subtitles to paper over an A/V
	// error that already exceeds GuardOffsetNS on its own.
	SilentSubtitleGuard bool
}

// DefaultThresholds are conservative broadcast-grade limits.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MaxOffsetNS:         40_000_000, // 40 ms audio/video lead/lag
		MaxDriftPPM:         50,
		MaxSubtitleShift:    200_000_000, // 200 ms
		GuardOffsetNS:       40_000_000,
		SilentSubtitleGuard: true,
	}
}

// AlignmentError pinpoints the earliest inconsistent position so operators
// can locate the fault on the shared timeline rather than re-deriving it.
type AlignmentError struct {
	Reason        string
	EarliestBadNS int64
	TrackA        TrackKind
	TrackB        TrackKind
	DeltaNS       int64
}

func (e *AlignmentError) Error() string {
	return fmt.Sprintf("sync: %s between %s and %s: delta=%dns earliest=%dns",
		e.Reason, e.TrackA, e.TrackB, e.DeltaNS, e.EarliestBadNS)
}

// Is lets errors.Is match the sentinel below on wrapped alignment failures.
func (e *AlignmentError) Is(target error) bool {
	_, ok := target.(*AlignmentError)
	return ok
}

// ErrAlignment is the marker every threshold failure wraps.
var ErrAlignment = errors.New("alignment threshold exceeded")

// Alignment is the verified result: per-track offsets plus fitted drift.
type Alignment struct {
	Offsets   map[string]int64 // trackID -> (shared - media) offset
	Drift     map[string]DriftModel
	SubShift  int64
	Earliest  int64
	Threshold Thresholds
}

// Aligner consumes anchors and observations and produces one verified
// alignment, failing fast with the earliest inconsistent time position.
type Aligner struct {
	th    Thresholds
	anchor map[TrackKind]Anchor
	obs   map[string][]Observation
	cues  []Cue
}

// NewAligner constructs an aligner with explicit thresholds.
func NewAligner(th Thresholds) *Aligner {
	return &Aligner{th: th, anchor: map[TrackKind]Anchor{}, obs: map[string][]Observation{}}
}

// AddAnchor registers the first valid sample/frame/cue of a track.
func (a *Aligner) AddAnchor(an Anchor) { a.anchor[an.Kind] = an }

// AddObservation feeds one drift fitting point for a track.
func (a *Aligner) AddObservation(trackID string, o Observation) {
	a.obs[trackID] = append(a.obs[trackID], o)
}

// AddCues registers subtitle cues for the silent-shift guard.
func (a *Aligner) AddCues(cues ...Cue) { a.cues = append(a.cues, cues...) }

// Align verifies anchors, fits drift per track, and applies the subtitle
// guard. The returned AlignmentError carries the earliest bad position.
func (a *Aligner) Align() (Alignment, error) {
	out := Alignment{
		Offsets:   map[string]int64{},
		Drift:     map[string]DriftModel{},
		Threshold: a.th,
		Earliest:  mathMaxInt64,
	}
	av, hasAV := a.anchor[TrackAudio]
	vid, hasVid := a.anchor[TrackVideo]
	// Per-track offsets.
	for _, an := range a.anchor {
		out.Offsets[an.TrackID] = an.SharedNS - an.MediaNS
	}
	// Audio vs video anchor agreement.
	if hasAV && hasVid {
		delta := (av.SharedNS - av.MediaNS) - (vid.SharedNS - vid.MediaNS)
		if abs64(delta) > a.th.MaxOffsetNS {
			earliest := min64(av.SharedNS, vid.SharedNS)
			return out, fmt.Errorf("%w: %w", ErrAlignment, &AlignmentError{
				Reason: "audio/video anchor offset exceeds threshold",
				EarliestBadNS: earliest, TrackA: TrackAudio, TrackB: TrackVideo, DeltaNS: delta,
			})
		}
		out.Earliest = min64(out.Earliest, min64(av.SharedNS, vid.SharedNS))
	}
	// Drift fitting per track.
	for id, points := range a.obs {
		if len(points) < 2 {
			continue
		}
		model, err := FitDrift(points)
		if err != nil {
			return out, err
		}
		if absFloat(model.DriftPPM) > a.th.MaxDriftPPM {
			earliest := points[0].SharedNS
			return out, fmt.Errorf("%w: %w", ErrAlignment, &AlignmentError{
				Reason: fmt.Sprintf("drift %.2fppm exceeds %.0fppm", model.DriftPPM, a.th.MaxDriftPPM),
				EarliestBadNS: earliest, DeltaNS: int64(model.DriftPPM),
			})
		}
		out.Drift[id] = model
	}
	// Subtitle handling: bounded shift, never masking an A/V failure.
	if sub, ok := a.anchor[TrackSubtitle]; ok {
		ref := sub.SharedNS
		if hasAV {
			ref = av.SharedNS
		} else if hasVid {
			ref = vid.SharedNS
		}
		shift := ref - sub.MediaNS - out.Offsets[sub.TrackID]
		if a.th.SilentSubtitleGuard && hasAV && hasVid {
			avDelta := abs64((av.SharedNS - av.MediaNS) - (vid.SharedNS - vid.MediaNS))
			if avDelta > a.th.GuardOffsetNS && abs64(shift) > 0 {
				return out, fmt.Errorf("%w: %w", ErrAlignment, &AlignmentError{
					Reason: "refusing subtitle shift that masks audio/video desync",
					EarliestBadNS: min64(sub.SharedNS, ref),
					TrackA: TrackSubtitle, TrackB: TrackAudio, DeltaNS: shift,
				})
			}
		}
		if abs64(shift) > a.th.MaxSubtitleShift {
			return out, fmt.Errorf("%w: %w", ErrAlignment, &AlignmentError{
				Reason: "subtitle shift exceeds bound",
				EarliestBadNS: sub.SharedNS, TrackA: TrackSubtitle, TrackB: TrackVideo, DeltaNS: shift,
			})
		}
		out.SubShift = shift
		out.Earliest = min64(out.Earliest, sub.SharedNS)
	}
	return out, nil
}

// ShiftCues returns copies of the cues moved by the verified subtitle shift.
func (al Alignment) ShiftCues(cues []Cue) []Cue {
	out := make([]Cue, len(cues))
	for i, c := range cues {
		c.StartNS += al.SubShift
		c.EndNS += al.SubShift
		out[i] = c
	}
	return out
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

const mathMaxInt64 = int64(^uint64(0) >> 1)
