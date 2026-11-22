package audio

import (
	"fmt"
	"sort"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

// Frame is one received audio frame with exact start time and declared size.
type Frame struct {
	Index int
	Start model.RationalTime
	// Duration is the frame's declared duration in seconds.
	Duration model.RationalTime
	Size     int
	Payload  []byte
}

// ReconcileOptions configures frame reconciliation.
type ReconcileOptions struct {
	Track model.TrackID
	// FrameTicks is the nominal frame duration at the track timescale.
	FrameTicks int64
	Timescale  uint32
	// MaxGapTicks is the largest gap that may be concealed. A larger gap makes
	// the track unrecoverable rather than silently shortened.
	MaxGapTicks int64
	// FullFrameBytes is the payload size of a complete frame. A final frame
	// smaller than this is a partial block: accepted when it is at least half
	// a frame, rejected below that. Zero disables the size check.
	FullFrameBytes int
}

// ReconcileResult records the explicit decision taken for each anomaly.
type ReconcileResult struct {
	Frames    []Frame
	Decisions []Decision
	Errors    []*model.Error
}

// DecisionKind classifies an audio frame repair.
type DecisionKind string

// Audio frame repair decisions.
const (
	DecisionGapConcealed DecisionKind = "gap-concealed"
	DecisionOverlapTrim  DecisionKind = "overlap-trimmed"
	DecisionDuplicate    DecisionKind = "duplicate-dropped"
	DecisionShortTail    DecisionKind = "short-tail-accepted"
	DecisionShortTailBad DecisionKind = "short-tail-rejected"
)

// Decision is one frame-level repair. Decisions are surfaced through the
// report in insertion order and are part of the deterministic output.
type Decision struct {
	Track   model.TrackID `json:"track"`
	Kind    DecisionKind  `json:"kind"`
	Index   int           `json:"index"`
	Ticks   int64         `json:"ticks"`
	Message string        `json:"message"`
}

// Reconcile orders frames by start time and resolves gaps, overlaps and a
// short final frame. Nothing is dropped silently: every departure from the
// nominal grid is reported.
func Reconcile(frames []Frame, opts ReconcileOptions) (ReconcileResult, error) {
	var res ReconcileResult
	if opts.Timescale == 0 || opts.FrameTicks <= 0 {
		return res, model.NewError(opts.Track, model.StageAudio, model.CodeInvalidParameter, false,
			"frame reconciliation needs a positive timescale and frame size")
	}
	grid, err := clock.FromTicks(opts.FrameTicks, opts.Timescale)
	if err != nil {
		return res, err
	}
	if len(frames) == 0 {
		e := model.NewError(opts.Track, model.StageAudio, model.CodeMissingPayload, false, "no audio frames")
		res.Errors = append(res.Errors, e)
		return res, e
	}

	sorted := append([]Frame(nil), frames...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, _ := clock.FromModel(sorted[i].Start)
		b, _ := clock.FromModel(sorted[j].Start)
		if c := a.Cmp(b); c != 0 {
			return c < 0
		}
		return sorted[i].Index < sorted[j].Index
	})

	var prevEnd clock.Rational
	var prevHas bool
	for _, f := range sorted {
		start, err := clock.FromModel(f.Start)
		if err != nil {
			return res, err
		}
		if prevHas {
			switch {
			case start.Cmp(prevEnd) < 0:
				overlap, err := prevEnd.Sub(start)
				if err != nil {
					return res, err
				}
				if overlap.Cmp(grid) >= 0 {
					res.Decisions = append(res.Decisions, Decision{Track: opts.Track, Kind: DecisionDuplicate, Index: f.Index,
						Message: fmt.Sprintf("frame starts %s before the previous frame ends; dropped as duplicate", overlap)})
					continue
				}
				// A strict overlap shorter than a frame is trimmed: the frame is
				// kept but starts at the previous frame's end so the timeline
				// stays gapless.
				trimmed := f
				trimmed.Start = clock.ToModel(prevEnd)
				f = trimmed
				res.Decisions = append(res.Decisions, Decision{Track: opts.Track, Kind: DecisionOverlapTrim, Index: f.Index,
					Message: fmt.Sprintf("frame overlapped the previous by %s; trimmed", overlap)})
			case start.Cmp(prevEnd) > 0:
				gap, err := start.Sub(prevEnd)
				if err != nil {
					return res, err
				}
				ticks, _ := gap.FloorTicks(opts.Timescale)
				if opts.MaxGapTicks > 0 && ticks > opts.MaxGapTicks {
					e := model.NewError(opts.Track, model.StageAudio, model.CodeAudioGapOverflow, false,
						"gap of %s exceeds the conceal limit of %d ticks", gap, opts.MaxGapTicks)
					res.Errors = append(res.Errors, e)
					return res, e
				}
				res.Frames = append(res.Frames, concealmentFrame(opts, prevEnd, gap, len(res.Frames)))
				res.Decisions = append(res.Decisions, Decision{Track: opts.Track, Kind: DecisionGapConcealed, Index: f.Index,
					Message: fmt.Sprintf("filled %s gap with concealment", gap)})
			}
		}
		start, err = clock.FromModel(f.Start)
		if err != nil {
			return res, err
		}
		end, err := start.Add(grid)
		if err != nil {
			return res, err
		}
		res.Frames = append(res.Frames, f)
		prevEnd = end
		prevHas = true
	}

	// A final frame is valid only if it is exactly one frame long and carries
	// a whole frame's payload. A short final frame is accepted when it is at
	// least half a frame (the encoder flushed a partial block) and rejected
	// otherwise. Both outcomes are recorded, never silently dropped.
	for _, e := range res.Errors {
		if !e.Recoverable {
			return res, e
		}
	}
	if len(res.Frames) > 0 {
		last := res.Frames[len(res.Frames)-1]
		dur, err := frameDuration(last, grid)
		if err != nil {
			return res, err
		}
		shortDur := dur.Cmp(grid) < 0
		shortSize := opts.FullFrameBytes > 0 && last.Size > 0 && last.Size*2 < opts.FullFrameBytes
		if shortDur || shortSize {
			half, err := grid.DivInt(2)
			if err != nil {
				return res, err
			}
			kind := DecisionShortTail
			msg := fmt.Sprintf("final frame is a partial block (duration %s, %d of %d bytes); accepted",
				dur, last.Size, opts.FullFrameBytes)
			// A tail below half a frame (by bytes) or below half the nominal
			// duration is rejected. Exactly half counts as a partial block.
			rejected := (shortSize && last.Size*2 < opts.FullFrameBytes/2) || (shortDur && dur.Cmp(half) < 0)
			if rejected {
				kind = DecisionShortTailBad
				msg = fmt.Sprintf("final frame is only %d of %d bytes (duration %s); rejected and tracked as a recoverable fault",
					last.Size, opts.FullFrameBytes, dur)
				res.Errors = append(res.Errors, model.NewError(opts.Track, model.StageAudio, model.CodeMissingPayload, false, "%s", msg))
			}
			res.Decisions = append(res.Decisions, Decision{Track: opts.Track, Kind: kind, Index: last.Index, Message: msg})
			if rejected {
				return res, res.Errors[len(res.Errors)-1]
			}
		}
	}
	return res, nil
}

// frameDuration returns a frame's declared duration, falling back to the
// nominal grid when the frame carries no explicit duration (for example when
// it was synthesized from a bare timestamp).
func frameDuration(f Frame, grid clock.Rational) (clock.Rational, error) {
	if f.Duration.Den == 0 {
		return grid, nil
	}
	d, err := clock.FromModel(f.Duration)
	if err != nil {
		return clock.Rational{}, err
	}
	if d.IsZero() {
		return grid, nil
	}
	return d, nil
}

func concealmentFrame(opts ReconcileOptions, at clock.Rational, gap clock.Rational, index int) Frame {
	dur := clock.ToModel(gap)
	return Frame{Index: -1 - index, Start: clock.ToModel(at), Duration: dur, Size: 0}
}
