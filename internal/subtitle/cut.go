package subtitle

import (
	"sort"

	"mediapack/internal/clock"
	"mediapack/internal/fault"
)

// Fragment is one half-open piece of a cue assigned to a media segment.
type Fragment struct {
	CueIndex int         `json:"cue_index"`
	Part     int         `json:"part"`
	Start    clock.Ticks `json:"start"`
	End      clock.Ticks `json:"end"`
	Text     string      `json:"text"`
}

// SplitCueAt cuts a cue at segment boundaries. Cuts outside the cue or on its
// edges are ignored. Intervals are half-open [Start, End). Text is divided by
// runes in order so concatenating fragment texts restores the source exactly.
func SplitCueAt(cue Cue, cuts []clock.Ticks) []Fragment {
	points := []clock.Ticks{cue.Start}
	for _, c := range cuts {
		if c > cue.Start && c < cue.End {
			points = append(points, c)
		}
	}
	points = append(points, cue.End)
	sort.Slice(points, func(i, j int) bool { return points[i] < points[j] })
	points = uniqueTicks(points)

	runes := []rune(cue.Text)
	n := len(points) - 1
	out := make([]Fragment, 0, n)
	for i := 0; i < n; i++ {
		startRune := len(runes) * i / n
		endRune := len(runes) * (i + 1) / n
		out = append(out, Fragment{
			CueIndex: cue.Index,
			Part:     i,
			Start:    points[i],
			End:      points[i+1],
			Text:     string(runes[startRune:endRune]),
		})
	}
	return out
}

func uniqueTicks(in []clock.Ticks) []clock.Ticks {
	out := in[:0]
	for _, v := range in {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}

// MergeFragments reconstructs a cue. It verifies part order and contiguous
// half-open boundaries; it never silently fills a gap or removes an overlap.
func MergeFragments(source string, fragments []Fragment) (Cue, error) {
	if len(fragments) == 0 {
		return Cue{}, fault.At(fault.SubtitleParse, source, 0, "cannot merge empty cue fragments")
	}
	parts := append([]Fragment(nil), fragments...)
	sort.SliceStable(parts, func(i, j int) bool {
		if parts[i].CueIndex != parts[j].CueIndex {
			return parts[i].CueIndex < parts[j].CueIndex
		}
		return parts[i].Part < parts[j].Part
	})
	cue := Cue{Index: parts[0].CueIndex, Start: parts[0].Start, Text: parts[0].Text}
	expectedPart := 0
	for i, p := range parts {
		if p.CueIndex != cue.Index {
			return Cue{}, fault.At(fault.SubtitleParse, source, int64(p.Start),
				"mixed cue indices %d and %d", cue.Index, p.CueIndex)
		}
		if p.Part != expectedPart {
			return Cue{}, fault.At(fault.SubtitleParse, source, int64(p.Start),
				"fragment part=%d expected=%d", p.Part, expectedPart)
		}
		if i > 0 && p.Start != parts[i-1].End {
			return Cue{}, fault.At(fault.SubtitleParse, source, int64(p.Start),
				"fragment gap/overlap: previous end=%d start=%d", parts[i-1].End, p.Start)
		}
		if p.End <= p.Start {
			return Cue{}, fault.At(fault.ZeroDurationCue, source, int64(p.Start),
				"merged fragment has non-positive duration")
		}
		if i > 0 {
			cue.Text += p.Text
		}
		cue.End = p.End
		expectedPart++
	}
	return cue, nil
}
