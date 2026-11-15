// Package overlap applies ordering policies to cues whose time spans collide
// on the same track. Cues from different tracks never interact.
package overlap

import (
	"fmt"
	"sort"
	"time"

	"subsync/internal/model"
)

// Strategy selects the collision policy.
type Strategy string

// Supported strategies.
const (
	// Keep leaves every cue untouched and only reports collisions.
	Keep Strategy = "keep"
	// Clip shortens a colliding cue so it starts at the previous cue's end.
	Clip Strategy = "clip"
	// Fail turns the first collision into a hard error.
	Fail Strategy = "fail"
)

// ParseStrategy resolves a user supplied policy name.
func ParseStrategy(s string) (Strategy, error) {
	switch s {
	case string(Keep), string(Clip), string(Fail):
		return Strategy(s), nil
	default:
		return "", fmt.Errorf("unknown overlap strategy %q (want keep|clip|fail)", s)
	}
}

// Result summarizes one Resolve run.
type Result struct {
	// Cues is the post-policy sequence in original order per track.
	Cues []model.Cue
	// Overlaps counts cues that intersect the preceding retained cue.
	Overlaps int
	// Dropped counts cues removed by clipping (post-clip duration <= 0).
	Dropped int
}

// Resolve groups cues by track and applies the strategy within each group.
// Relative ordering of cues is preserved; tracks themselves stay independent.
func Resolve(cues []model.Cue, strategy Strategy) (*Result, error) {
	tracks := groupByTrack(cues)
	out := &Result{}

	for _, track := range sortedTrackNames(tracks) {
		group := tracks[track]
		sort.SliceStable(group, func(i, j int) bool { return group[i].Order < group[j].Order })

		var lastEnd time.Duration
		haveLast := false
		for _, cue := range group {
			if haveLast && cue.Start < lastEnd {
				out.Overlaps++
				switch strategy {
				case Fail:
					return nil, fmt.Errorf("overlap on track %q: cue %q starts before prior cue ends",
						track, cueLabel(cue))
				case Clip:
					cue.Start = lastEnd
					if cue.Start >= cue.End {
						out.Dropped++
						continue
					}
				case Keep:
					// no mutation
				}
			}
			out.Cues = append(out.Cues, cue)
			lastEnd = cue.End
			haveLast = true
		}
	}
	return out, nil
}

func groupByTrack(cues []model.Cue) map[string][]model.Cue {
	m := map[string][]model.Cue{}
	for _, c := range cues {
		m[c.Track] = append(m[c.Track], c)
	}
	return m
}

func sortedTrackNames(m map[string][]model.Cue) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func cueLabel(c model.Cue) string {
	if c.ID != "" {
		return c.ID
	}
	return fmt.Sprintf("#%d", c.Order+1)
}
