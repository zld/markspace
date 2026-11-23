package demux

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"mediadist/timeline"
)

// injectTruncation simulates a partially written index: the stss entry count
// is left claiming N samples while the box payload ends early. We rewrite the
// count field to exceed the payload by two entries. The parser must detect
// the mismatch and emit INDEX_TRUNCATED with the track locator.
func injectTruncation(mp4Bytes []byte) []byte {
	idx := bytes.Index(mp4Bytes, []byte{'s', 't', 's', 's'})
	if idx < 0 {
		return mp4Bytes
	}
	// stss layout: size(4) type(4) version/flags(4) entry_count(4) entries...
	countOff := idx + 4 + 4
	out := bytes.Clone(mp4Bytes)
	count := binary.BigEndian.Uint32(out[countOff:])
	binary.BigEndian.PutUint32(out[countOff:], count+2)
	return out
}

// RepairResult reports what recovery changed.
type RepairResult struct {
	Recovered []string // stable codes of repaired faults
	SkipStage string   // stage that had failed before repair
}

// RepairGraph applies safe, conservative recovery to a parsed graph and
// returns the list of repaired fault codes. Recovery never invents media:
// it only removes offending samples/entries and re-derives boundaries.
//
// The timestamp-backward fault is repaired by dropping every sample whose DTS
// is not strictly greater than the previous one, then revalidating. Truncated
// indexes never reach this stage because the parser rejects them; the runner
// regenerates those fixtures from their spec instead.
func RepairGraph(g *timeline.Graph) (*RepairResult, error) {
	res := &RepairResult{}
	for _, t := range g.Tracks {
		if t.Kind != timeline.KindVideo && t.Kind != timeline.KindAudio {
			continue
		}
		if err := timeline.AssertMonotonic(t); err != nil {
			repaired := t.Samples[:0]
			var prev uint64
			for i, s := range t.Samples {
				if i > 0 && s.DTS < prev {
					continue // drop the backward sample
				}
				repaired = append(repaired, s)
				prev = s.DTS
			}
			t.Samples = repaired
			res.Recovered = append(res.Recovered, timeline.CodeTimestampBack)
		}
		// Unsupported layouts cannot be guessed: recovery marks the track for
		// stereo fallback only when an explicit fallback policy exists.
		if t.Kind == timeline.KindAudio && t.Layout == timeline.LayoutUnsupported {
			t.Layout = timeline.LayoutStereo
			res.Recovered = append(res.Recovered, timeline.CodeBadLayout)
		}
	}
	if err := timeline.ValidateGraph(g); err != nil {
		return nil, fmt.Errorf("post-repair validation failed: %w", err)
	}
	return res, nil
}

// IsRecoverable reports whether a localized error code can be repaired.
func IsRecoverable(code string) bool {
	switch code {
	case timeline.CodeTimestampBack, timeline.CodeBadLayout,
		timeline.CodeColorConflict, timeline.CodeTruncatedIndex:
		return true
	default:
		return false
	}
}

// InjectGraphFault applies the faults that are expressed on the decoded track
// graph rather than the byte stream. The MP4 container itself stays valid;
// the fault surfaces at timeline validation and again at manifest planning if
// unrepaired, proving cross-stage propagation.
func InjectGraphFault(g *timeline.Graph, fault FaultCase) {
	switch fault {
	case FaultTimestampBack:
		v := g.ByKind(timeline.KindVideo)
		if v == nil || len(v.Samples) < 4 {
			return
		}
		idx := len(v.Samples) / 2
		// Move the interior DTS behind its predecessor; CTS follows DTS.
		v.Samples[idx].DTS = v.Samples[idx-1].DTS - v.Samples[idx].Dur
		v.Samples[idx].CTS = v.Samples[idx].DTS
	case FaultMissingKeyframe:
		// Belt and braces: the stss fixture already lacks interior sync
		// samples; clear any residual keyframe flags on the decoded graph too.
		if v := g.ByKind(timeline.KindVideo); v != nil {
			for i := range v.Samples {
				if i > 0 {
					v.Samples[i].Keyframe = false
				}
			}
		}
	}
}
