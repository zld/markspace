// Package overlap resolves collisions between frame-aligned cues.
//
// Cues arrive in source file order, which is also the tie-break order for cues
// sharing a start frame. Three policies are supported:
//
//   - keep:       leave every interval untouched, overlaps remain;
//   - trim:       shorten an earlier cue so it ends where the next cue starts;
//   - drop-later: discard the later cue when an overlap is detected.
//
// When trimming leaves fewer than one frame, the shrunken cue is itself
// resolved again using the policy (kept as-is under keep, dropped under the
// other two policies).
package overlap

import (
	"fmt"

	"subtitle/internal/timebase"
)

// Strategy names accepted on the command line.
const (
	Keep      = "keep"
	Trim      = "trim"
	DropLater = "drop-later"
)

// ParseStrategy validates a user supplied policy name.
func ParseStrategy(s string) (string, error) {
	switch s {
	case Keep, Trim, DropLater:
		return s, nil
	default:
		return "", fmt.Errorf("unknown overlap strategy %q (want keep, trim or drop-later)", s)
	}
}

// Item is a frame-mapped cue awaiting conflict resolution.
type Item struct {
	Index  int // 1-based position in the source document
	Frames timebase.Frames
}

// Outcome records how one input item was handled.
type Outcome struct {
	Index   int
	Frames  timebase.Frames
	Dropped bool
	Reason  string // empty for surviving cues
}

// Resolve applies the policy to items in their source order.
func Resolve(items []Item, strategy string) ([]Outcome, error) {
	if _, err := ParseStrategy(strategy); err != nil {
		return nil, err
	}
	outcomes := make([]Outcome, 0, len(items))
	// lastKept is the most recent surviving interval; because inputs are
	// ordered by start frame, only it can overlap the current item.
	var lastKept *timebase.Frames
	for _, it := range items {
		cur := it.Frames
		oc := Outcome{Index: it.Index, Frames: cur}
		if lastKept != nil && cur.Start < lastKept.End && cur.End > lastKept.Start {
			switch strategy {
			case Keep:
				// Nothing changes; overlaps are intentionally preserved.
			case Trim:
				cur.Start = lastKept.End
				oc.Frames = cur
				if cur.End <= cur.Start {
					oc.Dropped = true
					oc.Reason = "trimmed below one frame"
				}
			case DropLater:
				oc.Dropped = true
				oc.Reason = "overlaps earlier cue"
			}
		}
		if !oc.Dropped {
			kept := oc.Frames
			lastKept = &kept
		}
		outcomes = append(outcomes, oc)
	}
	return outcomes, nil
}

// Dropped counts the dropped outcomes.
func Dropped(outcomes []Outcome) int {
	n := 0
	for _, o := range outcomes {
		if o.Dropped {
			n++
		}
	}
	return n
}
