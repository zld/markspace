// Package recovery rebuilds auditable coverage per representation layer. It
// skips recoverable damage and resynchronizes at the next verified keyframe;
// unrecoverable truncation becomes a retained gap, never invented content.
package recovery

import (
	"fmt"
	"sort"

	"archivecheck/internal/index"
	"archivecheck/internal/media"
)

// Unit is one parsed frame-bearing unit with absolute byte bounds. Offsets are
// the unit's start code/OBU header; End is one past its payload.
type Unit struct {
	Offset    int64
	End       int64
	Keyframe  bool
	Truncated bool
}

// Truncation describes unrecoverable end-of-stream damage detected by a
// parser.
type Truncation struct {
	Offset int64
	Kind   media.FaultKind
	Detail string
}

// Input is one layer's evidence for recovery.
type Input struct {
	Layer      string
	Clock      media.Clock
	StreamLen  int64
	DurationMs int64
	Units      []Unit
	Truncation *Truncation
	Index      index.Validation
}

// Anchor is a keyframe position whose time and byte boundary both verified.
type Anchor struct {
	TimeMs int64
	Offset int64
}

// Segment is a contiguous recovered span.
type Segment struct {
	StartMs   int64
	EndMs     int64
	ByteStart int64
	ByteEnd   int64
}

// Gap is a retained hole with an attributable cause.
type Gap struct {
	StartMs   int64
	EndMs     int64
	ByteStart int64
	ByteEnd   int64
	Reason    media.FaultKind
	Detail    string
	Class     media.RecoveryClass
}

// Result is one layer's recovered coverage.
type Result struct {
	Layer          string
	Clock          media.Clock
	StreamLen      int64
	DurationMs     int64
	Anchors        []Anchor
	Segments       []Segment
	Gaps           []Gap
	Frames         int
	Parseable      int
	ParseRatio     float64
	MeanKFMs       float64
	MaxKFMs        int64
	StartMs        int64
	EndMs          int64
	RecoveredBytes int64
	Faults         []media.Fault
}

// Recover builds coverage for one layer.
func Recover(in Input) Result {
	res := Result{Layer: in.Layer, Clock: in.Clock, StreamLen: in.StreamLen, DurationMs: in.DurationMs}
	res.Frames, res.Parseable = countFrames(in.Units)
	if res.Frames > 0 {
		res.ParseRatio = float64(res.Parseable) / float64(res.Frames)
	}

	kfUnits := map[int64]bool{}
	for _, u := range in.Units {
		if u.Keyframe && !u.Truncated {
			kfUnits[u.Offset] = true
		}
	}

	verified := map[int64]bool{}
	for _, a := range in.Index.Aligned {
		if !kfUnits[a.Boundary] {
			res.Faults = append(res.Faults, media.NewFault(media.KindBoundaryDrift, in.Layer,
				fmt.Sprintf("index entry at line %d maps to offset %d which is not an intact keyframe", a.Entry.Line, a.Boundary),
				a.Boundary, media.Recoverable))
			continue
		}
		verified[a.Boundary] = true
		res.Anchors = append(res.Anchors, Anchor{TimeMs: in.Clock.Millis(a.Entry.PTS), Offset: a.Boundary})
	}
	sort.SliceStable(res.Anchors, func(i, j int) bool { return res.Anchors[i].Offset < res.Anchors[j].Offset })

	if len(res.Anchors) == 0 {
		res.Faults = append(res.Faults, media.NewFault(media.KindLayerUnusable, in.Layer,
			"no verified keyframe anchor remains", -1, media.Unrecoverable))
		res.Gaps = append(res.Gaps, Gap{StartMs: 0, EndMs: in.DurationMs, ByteStart: 0, ByteEnd: in.StreamLen,
			Reason: media.KindLayerUnusable, Detail: "no verified keyframe anchor", Class: media.Unrecoverable})
		return res
	}

	res.StartMs = res.Anchors[0].TimeMs
	res.EndMs = in.DurationMs
	res.MeanKFMs, res.MaxKFMs = intervals(res.Anchors)

	// Unverified keyframe units mark time regions that cannot be certified,
	// because no index entry ties them to the shared clock.
	var unverified []int64
	for _, u := range in.Units {
		if u.Keyframe && !u.Truncated && !verified[u.Offset] {
			unverified = append(unverified, u.Offset)
		}
	}
	sort.Slice(unverified, func(i, j int) bool { return unverified[i] < unverified[j] })

	for i, a := range res.Anchors {
		endMs := in.DurationMs
		nextOff := in.StreamLen
		var nextVerified *Anchor
		if i+1 < len(res.Anchors) {
			nextVerified = &res.Anchors[i+1]
			endMs = nextVerified.TimeMs
			nextOff = nextVerified.Offset
		}
		if endMs < a.TimeMs {
			endMs = a.TimeMs
		}

		// A damaged tail inside this interval makes the whole interval a gap:
		// partial interval content is retained as evidence, not spliced.
		if in.Truncation != nil && in.Truncation.Offset >= a.Offset && (nextVerified == nil || in.Truncation.Offset < nextVerified.Offset) {
			kind := in.Truncation.Kind
			if kind == "" {
				kind = media.KindGap
			}
			detail := in.Truncation.Detail
			if detail == "" {
				detail = fmt.Sprintf("unrecoverable truncation at byte %d", in.Truncation.Offset)
			}
			res.Gaps = append(res.Gaps, Gap{StartMs: a.TimeMs, EndMs: endMs, ByteStart: a.Offset, ByteEnd: in.StreamLen,
				Reason: kind, Detail: detail, Class: media.Unrecoverable})
			res.Faults = append(res.Faults, media.NewFault(media.KindGap, in.Layer,
				fmt.Sprintf("gap [%d,%d) ms retained after truncation", a.TimeMs, endMs), in.Truncation.Offset, media.Unrecoverable).
				WithSpan(in.span(a.TimeMs, endMs)))
			continue
		}

		// An unverified keyframe inside this interval defeats certification of
		// the interval: skip forward to the next verified anchor.
		if hasBetween(unverified, a.Offset, nextOff) {
			res.Gaps = append(res.Gaps, Gap{StartMs: a.TimeMs, EndMs: endMs, ByteStart: a.Offset, ByteEnd: nextOff,
				Reason: media.KindIndexOutOfRange, Detail: "keyframe boundary present but unverified by the index", Class: media.Recoverable})
			res.Faults = append(res.Faults, media.NewFault(media.KindGap, in.Layer,
				fmt.Sprintf("gap [%d,%d) ms retained while resynchronizing to the next verified keyframe", a.TimeMs, endMs),
				a.Offset, media.Recoverable).WithSpan(in.span(a.TimeMs, endMs)))
			continue
		}

		res.Segments = append(res.Segments, Segment{StartMs: a.TimeMs, EndMs: endMs, ByteStart: a.Offset, ByteEnd: nextOff})
		res.RecoveredBytes += nextOff - a.Offset
	}

	// A keyframe before the first verified anchor leaves a leading gap.
	if first := unverifiedBefore(unverified, res.Anchors[0].Offset); first >= 0 {
		res.Gaps = append([]Gap{{StartMs: 0, EndMs: res.Anchors[0].TimeMs, ByteStart: 0, ByteEnd: res.Anchors[0].Offset,
			Reason: media.KindIndexOutOfRange, Detail: "leading keyframes are not verified by the index", Class: media.Recoverable}}, res.Gaps...)
		res.Faults = append(res.Faults, media.NewFault(media.KindGap, in.Layer,
			fmt.Sprintf("leading gap [0,%d) ms retained before the first verified keyframe", res.Anchors[0].TimeMs),
			0, media.Recoverable))
	}

	if res.ParseRatio < 1 && len(res.Gaps) == 0 {
		res.Faults = append(res.Faults, media.NewFault(media.KindGap, in.Layer,
			"parseable ratio below 1 but no gap was attributable; refusing to claim continuity",
			-1, media.Unrecoverable))
	}
	return res
}

// span renders a millisecond interval on the layer clock for fault evidence.
func (in Input) span(startMs, endMs int64) media.TickInterval {
	return media.NewInterval(in.Clock.Ticks(startMs), in.Clock.Ticks(endMs), in.Clock)
}

func countFrames(units []Unit) (frames, parseable int) {
	for _, u := range units {
		frames++
		if !u.Truncated && u.End > u.Offset {
			parseable++
		}
	}
	return frames, parseable
}

func intervals(anchors []Anchor) (mean float64, max int64) {
	if len(anchors) < 2 {
		return 0, 0
	}
	var sum int64
	max = 0
	for i := 1; i < len(anchors); i++ {
		d := anchors[i].TimeMs - anchors[i-1].TimeMs
		if d < 0 {
			d = -d
		}
		sum += d
		if d > max {
			max = d
		}
	}
	return float64(sum) / float64(len(anchors)-1), max
}

func hasBetween(sorted []int64, lo, hi int64) bool {
	for _, v := range sorted {
		if v > lo && v < hi {
			return true
		}
	}
	return false
}

func unverifiedBefore(sorted []int64, off int64) int64 {
	for _, v := range sorted {
		if v < off {
			return v
		}
	}
	return -1
}
