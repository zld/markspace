package pipeline

import (
	"fmt"

	"subtitle/internal/codec"
	"subtitle/internal/cue"
	"subtitle/internal/overlap"
	"subtitle/internal/parser"
	"subtitle/internal/report"
	"subtitle/internal/timebase"
)

// toleranceMS returns the maximum allowed round-trip timing deviation: ASS
// stores centiseconds (up to 5 ms of rounding), the other formats milliseconds
// (up to 1 ms).
func toleranceMS(format cue.Format) int64 {
	if format == cue.FormatASS {
		return 5
	}
	return 1
}

func buildOutput(
	src []cue.Cue,
	outcomes []overlap.Outcome,
	rate timebase.Rate,
	to, roundtrip cue.Format,
) (string, []codec.OutputCue, []report.CueLine, int64, error) {
	var outCues []codec.OutputCue
	// surviving maps output position -> source cue index (0-based).
	var surviving []int
	for _, oc := range outcomes {
		if oc.Dropped {
			continue
		}
		idx := oc.Index - 1
		outCues = append(outCues, codec.OutputCue{Frames: oc.Frames, Text: src[idx].Text})
		surviving = append(surviving, idx)
	}

	serialized, err := codec.Serialize(outCues, to, rate)
	if err != nil {
		return "", nil, nil, 0, err
	}

	// Re-parse the serialized output with the matching self-hosted parser.
	parsed, err := reparse(serialized, to)
	if err != nil {
		return "", nil, nil, 0, fmt.Errorf("re-parse output: %w", err)
	}
	if len(parsed.Cues) != len(outCues) {
		return "", nil, nil, 0, fmt.Errorf("round-trip count mismatch: wrote %d cues, read back %d",
			len(outCues), len(parsed.Cues))
	}

	lines := make([]report.CueLine, len(outcomes))
	var maxDelta int64
	tol := toleranceMS(to)
	for i, oc := range outcomes {
		line := report.CueLine{Cue: oc.Index, StartFrame: oc.Frames.Start, EndFrame: oc.Frames.End, Dropped: oc.Dropped}
		if !oc.Dropped {
			outPos := outputPosition(surviving, oc.Index-1)
			back := parsed.Cues[outPos]
			if back.Text != src[oc.Index-1].Text {
				return "", nil, nil, 0, fmt.Errorf("cue %d text changed in round-trip: %q != %q",
					oc.Index, src[oc.Index-1].Text, back.Text)
			}
			wantStart := rate.FrameTime(oc.Frames.Start)
			wantEnd := rate.FrameTime(oc.Frames.End)
			dStart := timebase.RoundMillis(back.Start - wantStart)
			dEnd := timebase.RoundMillis(back.End - wantEnd)
			delta := report.AbsInt64(dStart)
			if d := report.AbsInt64(dEnd); d > delta {
				delta = d
			}
			if delta > tol {
				return "", nil, nil, 0, fmt.Errorf("cue %d round-trip delta %dms exceeds %dms tolerance",
					oc.Index, delta, tol)
			}
			line.RoundtripDeltaMS = delta
			if delta > maxDelta {
				maxDelta = delta
			}
		}
		lines[i] = line
	}

	// Optional second leg: re-serialize back into the source format and parse
	// again, proving the conversion is reversible end to end.
	if roundtrip != "" && roundtrip != to {
		if err := verifySecondLeg(outCues, roundtrip, rate, tol); err != nil {
			return "", nil, nil, 0, err
		}
	}
	return serialized, outCues, lines, maxDelta, nil
}

func outputPosition(surviving []int, sourceIdx int) int {
	for i, s := range surviving {
		if s == sourceIdx {
			return i
		}
	}
	return -1
}

func reparse(text string, format cue.Format) (*cue.Document, error) {
	switch format {
	case cue.FormatSRT:
		return parser.ParseSRT("roundtrip.srt", text)
	case cue.FormatVTT:
		return parser.ParseVTT("roundtrip.vtt", text)
	case cue.FormatASS:
		return parser.ParseASS("roundtrip.ass", text)
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}

func verifySecondLeg(outCues []codec.OutputCue, target cue.Format, rate timebase.Rate, tol int64) error {
	text, err := codec.Serialize(outCues, target, rate)
	if err != nil {
		return fmt.Errorf("second-leg serialize: %w", err)
	}
	doc, err := reparse(text, target)
	if err != nil {
		return fmt.Errorf("second-leg parse: %w", err)
	}
	if len(doc.Cues) != len(outCues) {
		return fmt.Errorf("second-leg count mismatch: %d != %d", len(doc.Cues), len(outCues))
	}
	for i, c := range doc.Cues {
		if c.Text != outCues[i].Text {
			return fmt.Errorf("second-leg cue %d text mismatch", i+1)
		}
		delta := report.AbsInt64(timebase.RoundMillis(c.Start - rate.FrameTime(outCues[i].Frames.Start)))
		if delta > tol {
			return fmt.Errorf("second-leg cue %d delta %dms exceeds tolerance %dms", i+1, delta, tol)
		}
	}
	return nil
}
