package timeline

import "fmt"

// Stage names used for fault localization across pipeline layers.
const (
	StageDemux     = "demux"
	StageTimeline  = "timeline"
	StageNormalize = "normalize"
	StageSegment   = "segment"
	StageManifest  = "manifest"
)

// LocError carries a machine-readable code, the failing pipeline stage and a
// track/scene locator so faults can be traced across parse, timeline and
// manifest stages without string matching.
type LocError struct {
	Code      string // stable error code, e.g. "TIMESTAMP_BACKWARD"
	Stage     string
	TrackID   uint32
	SampleIdx int // -1 when not sample-specific
	Op        string
	Err       error
}

func (e *LocError) Error() string {
	loc := "track=-"
	if e.TrackID != 0 {
		loc = fmt.Sprintf("track=%d", e.TrackID)
	}
	if e.SampleIdx >= 0 {
		loc += fmt.Sprintf(" sample=%d", e.SampleIdx)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: stage=%s %s op=%s: %s", e.Code, e.Stage, loc, e.Op, e.Err)
	}
	return fmt.Sprintf("%s: stage=%s %s op=%s", e.Code, e.Stage, loc, e.Op)
}

func (e *LocError) Unwrap() error { return e.Err }

// NewLocError builds a localized error.
func NewLocError(code, stage string, trackID uint32, sampleIdx int, op string, err error) *LocError {
	return &LocError{Code: code, Stage: stage, TrackID: trackID, SampleIdx: sampleIdx, Op: op, Err: err}
}

// Stable error codes. Keep these fixed: tests and exit-code mapping depend on them.
const (
	CodeTruncatedIndex  = "INDEX_TRUNCATED"
	CodeTimestampBack   = "TIMESTAMP_BACKWARD"
	CodeBadLayout       = "LAYOUT_UNSUPPORTED"
	CodeColorConflict   = "COLOR_CONFLICT"
	CodeColorPending    = "COLOR_PENDING"
	CodeMissingKeyframe = "KEYFRAME_MISSING"
	CodeAlignmentDrift  = "ALIGNMENT_DRIFT"
	CodeDurationConserv = "DURATION_NOT_CONSERVED"
	CodeUncheckedInput  = "INPUT_LIMIT_EXCEEDED"
	CodeBadArgument     = "BAD_ARGUMENT"
	CodeCheckpoint      = "CHECKPOINT_CORRUPT"
)

// ValidateGraph checks every timeline invariant and returns the first
// violation as a LocError. It never repairs: recovery policy lives in demux.
func ValidateGraph(g *Graph) error {
	if g == nil || len(g.Tracks) == 0 {
		return NewLocError("GRAPH_EMPTY", StageTimeline, 0, -1, "validate", nil)
	}
	for _, t := range g.Tracks {
		if err := validateTrack(t); err != nil {
			return err
		}
	}
	if err := validateEnds(g); err != nil {
		return err
	}
	return nil
}

func validateTrack(t *Track) error {
	if t.Timescale == 0 {
		return NewLocError(CodeBadArgument, StageTimeline, t.ID, -1, "timescale", nil)
	}
	prevDTS := uint64(0)
	for i, s := range t.Samples {
		if s.Dur == 0 {
			return NewLocError("ZERO_DURATION", StageTimeline, t.ID, i, "sample-dur", nil)
		}
		if i > 0 && s.DTS < prevDTS {
			return NewLocError(CodeTimestampBack, StageTimeline, t.ID, i,
				"dts-monotonic", fmt.Errorf("dts=%d prev=%d", s.DTS, prevDTS))
		}
		if s.CTS < s.DTS && s.DTS-s.CTS > MaxCommonTicks {
			return NewLocError("CTS_OUT_OF_RANGE", StageTimeline, t.ID, i, "cts-offset", nil)
		}
		prevDTS = s.DTS
	}
	switch t.Kind {
	case KindAudio:
		if t.Layout == LayoutUnknown || t.Layout == LayoutUnsupported {
			return NewLocError(CodeBadLayout, StageTimeline, t.ID, -1,
				"channel-layout", fmt.Errorf("layout=%d", t.Layout))
		}
		if t.SampleRate == 0 {
			return NewLocError("SAMPLE_RATE_ZERO", StageTimeline, t.ID, -1, "audio", nil)
		}
	case KindVideo:
		// A nil color marker means "absent": normalization decides pending vs.
		// resolved; validation itself must not invent metadata.
	default:
		return NewLocError(CodeBadArgument, StageTimeline, t.ID, -1, "track-kind", nil)
	}
	return nil
}

// validateEnds enforces the conservation invariant every manifest must later
// satisfy: all tracks terminate within one common tick of the graph end.
func validateEnds(g *Graph) error {
	var end uint64
	for _, t := range g.Tracks {
		if e := t.End(); e > end {
			end = e
		}
	}
	if end > MaxCommonTicks {
		return NewLocError(CodeUncheckedInput, StageTimeline, 0, -1, "max-duration", nil)
	}
	for _, t := range g.Tracks {
		if d := end - t.End(); d > 1 {
			return NewLocError(CodeDurationConserv, StageTimeline, t.ID, -1,
				"track-end", fmt.Errorf("track_end=%d graph_end=%d drift=%d", t.End(), end, d))
		}
	}
	return nil
}

// AssertMonotonic is a reusable strict-DTS check used by recovery and tests.
func AssertMonotonic(t *Track) error {
	for i := 1; i < len(t.Samples); i++ {
		if t.Samples[i].DTS < t.Samples[i-1].DTS {
			return NewLocError(CodeTimestampBack, StageTimeline, t.ID, i, "dts-monotonic", nil)
		}
	}
	return nil
}
