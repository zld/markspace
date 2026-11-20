// Package verify re-parses serialized output and checks it against the
// cues that were written, computing per-cue round-trip time deltas.
package verify

import (
	"fmt"

	"subx/internal/format"
	"subx/internal/timeline"
)

// MismatchError reports a serialize/re-parse inconsistency (exit code 4).
type MismatchError struct{ Msg string }

func (e *MismatchError) Error() string { return e.Msg }

// CueResult is the verification outcome for one output cue.
type CueResult struct {
	DeltaMs int64 // max boundary delta vs the original input times
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// Check parses serialized output of the given format and verifies count,
// text and timing against the kept frame cues. It returns per-cue
// round-trip deltas (original ms vs re-parsed ms) and the max abs delta.
func Check(serialized []byte, to format.Format, kept []timeline.FrameCue, fps timeline.FPS, file string) ([]CueResult, int64, error) {
	back, err := format.Parse(to, string(serialized), file)
	if err != nil {
		return nil, 0, &MismatchError{Msg: fmt.Sprintf("re-parse of serialized output failed: %v", err)}
	}
	if len(back) != len(kept) {
		return nil, 0, &MismatchError{Msg: fmt.Sprintf("re-parsed %d cues, expected %d", len(back), len(kept))}
	}
	results := make([]CueResult, len(kept))
	var maxDelta int64
	for i, fc := range kept {
		got := back[i]
		if len(got.Lines) != len(fc.Lines) {
			return nil, 0, &MismatchError{Msg: fmt.Sprintf("cue %q: re-parsed %d text lines, expected %d", fc.ID, len(got.Lines), len(fc.Lines))}
		}
		for j := range got.Lines {
			if got.Lines[j] != fc.Lines[j] {
				return nil, 0, &MismatchError{Msg: fmt.Sprintf("cue %q line %d: text changed in round trip", fc.ID, j+1)}
			}
		}
		wantStart, wantEnd := fps.FrameStartMs(fc.StartFrame), fps.FrameEndMs(fc.EndFrame)
		if got.StartMs != wantStart || got.EndMs != wantEnd {
			return nil, 0, &MismatchError{Msg: fmt.Sprintf(
				"cue %q: re-parsed times %d-%dms, expected %d-%dms",
				fc.ID, got.StartMs, got.EndMs, wantStart, wantEnd)}
		}
		d := abs(got.StartMs - fc.Cue.StartMs)
		if e := abs(got.EndMs - fc.Cue.EndMs); e > d {
			d = e
		}
		results[i] = CueResult{DeltaMs: d}
		if d > maxDelta {
			maxDelta = d
		}
	}
	return results, maxDelta, nil
}
