package repair

import (
	"fmt"

	"replayproof/internal/media"
	"replayproof/internal/parse"
)

// FixKind classifies a timestamp repair.
type FixKind string

const (
	FixNone      FixKind = "none"
	FixMissing   FixKind = "missing"
	FixDuplicate FixKind = "duplicate"
	FixBackwards FixKind = "backwards"
)

// TimestampFix preserves the value before and after a repair so the delivery
// summary can be audited sample by sample.
type TimestampFix struct {
	TrackID      uint32
	SampleNumber int
	Kind         FixKind
	BeforeDTS    media.Rational
	AfterDTS     media.Rational
	BeforeDur    media.Rational
	AfterDur     media.Rational
}

// Policy controls how gaps in the timestamp sequence are filled.
type Policy struct {
	// DefaultDuration is used when the first sample lacks a duration and no
	// earlier duration exists. Zero means such input is an error.
	DefaultDuration media.Rational
}

// Result carries the repaired sample sequence and the audit trail.
type Result struct {
	Samples []parse.Sample
	Fixes   []TimestampFix
}

// Counts returns the number of fixes by kind for summarisation.
func (r *Result) Counts() map[FixKind]int {
	out := make(map[FixKind]int, 4)
	for _, f := range r.Fixes {
		out[f.Kind]++
	}
	return out
}

// RepairTrack rebuilds a monotonic decode timeline.
//
// Rules, in order:
//   - A zero or negative duration is "missing" and inherits the previous
//     sample's repaired duration (or Policy.DefaultDuration for sample 1).
//   - A sample whose DTS is not strictly after the previous sample's end is
//     "duplicate" (equal DTS) or "backwards" (earlier DTS) and is moved to
//     that end.
//   - A DTS at or after the previous end is legal and is preserved exactly,
//     including intentional gaps.
//
// PTS keeps its offset from DTS when DTS moves; a PTS that would precede the
// new DTS is pinned to it so presentation order remains representable.
func RepairTrack(track *parse.Track, policy Policy) (*Result, error) {
	if track == nil {
		return nil, media.New(media.CodeConfig, "repair.track", -1, "nil track")
	}
	res := &Result{Samples: make([]parse.Sample, len(track.Samples))}
	copy(res.Samples, track.Samples)
	var prevEnd media.Rational
	var prevDur media.Rational
	for i := range res.Samples {
		s := &res.Samples[i]
		origDTS := s.DTS
		origDur := s.Duration
		kind := FixNone

		if s.Duration.Sign() <= 0 {
			if prevDur.Sign() > 0 {
				s.Duration = prevDur
			} else if policy.DefaultDuration.Sign() > 0 {
				s.Duration = policy.DefaultDuration
			} else {
				return nil, &media.Error{
					Code: media.CodeBadTimestamp, Op: "repair.duration",
					TrackID: track.ID, Offset: s.Offset,
					Msg: fmt.Sprintf("sample %d has no duration and no predecessor", s.Number),
				}
			}
			kind = FixMissing
		}

		if i > 0 {
			prevDTS := res.Samples[i-1].DTS
			if s.DTS.Cmp(prevEnd) < 0 {
				switch {
				case origDTS.Cmp(prevDTS) == 0:
					kind = FixDuplicate
				default:
					kind = FixBackwards
				}
				shift := prevEnd.Sub(s.DTS)
				s.DTS = prevEnd
				s.PTS = s.PTS.Add(shift)
				if s.PTS.Cmp(s.DTS) < 0 {
					s.PTS = s.DTS
				}
			}
		}

		if kind != FixNone {
			res.Fixes = append(res.Fixes, TimestampFix{
				TrackID: track.ID, SampleNumber: s.Number, Kind: kind,
				BeforeDTS: origDTS, AfterDTS: s.DTS,
				BeforeDur: origDur, AfterDur: s.Duration,
			})
		}
		prevEnd = s.End()
		prevDur = s.Duration
	}
	return res, nil
}

// ValidateMonotonic reports the first sample whose decode interval overlaps
// its predecessor. It is used after repair to prove the invariant holds.
func ValidateMonotonic(samples []parse.Sample) error {
	for i := 1; i < len(samples); i++ {
		if samples[i].DTS.Less(samples[i-1].End()) {
			return &media.Error{
				Code:    media.CodeBadTimestamp,
				Op:      "repair.validate",
				Offset:  samples[i].Offset,
				HasTime: true,
				From:    samples[i].DTS,
				To:      samples[i].End(),
				Msg:     fmt.Sprintf("sample %d overlaps sample %d", samples[i].Number, samples[i-1].Number),
			}
		}
		if samples[i].Duration.Sign() <= 0 {
			return &media.Error{
				Code:    media.CodeBadTimestamp,
				Op:      "repair.validate",
				Offset:  samples[i].Offset,
				HasTime: true,
				From:    samples[i].DTS,
				To:      samples[i].End(),
				Msg:     fmt.Sprintf("sample %d has non-positive duration", samples[i].Number),
			}
		}
	}
	return nil
}
