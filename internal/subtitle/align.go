package subtitle

import (
	"fmt"
	"sort"

	"mediaaudit/internal/model"
)

// Overlap records two cues whose spans intersect.
type Overlap struct {
	A, B int // cue indices into the input slice
}

// Overlaps reports every pair of cues whose half-open spans intersect.
// Input order is preserved in diagnostics; the returned pairs are sorted by
// (A, B) so repeated runs agree. Cues are expected in document order but the
// function does not require it.
func Overlaps(cues []Cue) ([]Overlap, model.Errors) {
	var out []Overlap
	var es model.Errors
	for i := 0; i < len(cues); i++ {
		for j := i + 1; j < len(cues); j++ {
			a, b := cues[i], cues[j]
			if a.Start.Cmp(b.End) >= 0 || b.Start.Cmp(a.End) >= 0 {
				continue
			}
			out = append(out, Overlap{A: i, B: j})
			es.Add(model.NewError(model.CodeSubtitleOverlap, "subtitle", "", b.Pos,
				"cue %d (%s --> %s) overlaps cue %d (%s --> %s)",
				b.Index, b.StartText, b.EndText, a.Index, a.StartText, a.EndText))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out, es
}

// SplitAtCuts divides a cue at every cut point strictly inside its span.
// The returned pieces tile the cue exactly and keep the original text on each
// piece, so callers can hash per-piece content. Cuts are frame boundaries
// expressed on the same clock as the cue.
func SplitAtCuts(cue Cue, cuts []model.Rat) ([]Cue, error) {
	if cue.End.Cmp(cue.Start) < 0 {
		return nil, model.NewError(model.CodeSubtitleReversed, "subtitle", "", cue.Pos,
			"cue %d ends before it starts", cue.Index)
	}
	var inside []model.Rat
	seen := make(map[string]bool, len(cuts))
	for _, c := range cuts {
		if c.Cmp(cue.Start) <= 0 || c.Cmp(cue.End) >= 0 {
			continue
		}
		k := c.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		inside = append(inside, c)
	}
	if len(inside) == 0 {
		return []Cue{cue}, nil
	}
	sort.Slice(inside, func(i, j int) bool { return inside[i].Cmp(inside[j]) < 0 })
	pieces := make([]Cue, 0, len(inside)+1)
	boundaries := append([]model.Rat{cue.Start}, inside...)
	boundaries = append(boundaries, cue.End)
	for i := 0; i+1 < len(boundaries); i++ {
		p := cue
		p.Start = boundaries[i]
		p.End = boundaries[i+1]
		p.StartText = cue.StartText
		p.EndText = cue.EndText
		pieces = append(pieces, p)
	}
	return pieces, nil
}

// Normalize sorts cues by start time (ties broken by end, then original
// index) and returns the stable order. It does not mutate the input.
func Normalize(cues []Cue) []Cue {
	out := make([]Cue, len(cues))
	copy(out, cues)
	sort.SliceStable(out, func(i, j int) bool {
		if c := out[i].Start.Cmp(out[j].Start); c != 0 {
			return c < 0
		}
		if c := out[i].End.Cmp(out[j].End); c != 0 {
			return c < 0
		}
		return out[i].Index < out[j].Index
	})
	return out
}

// CutPointsFromBoundaries converts a sorted boundary list into the cut set
// used for splitting; duplicates are removed and order preserved.
func CutPointsFromBoundaries(bounds []model.Rat) []model.Rat {
	if len(bounds) == 0 {
		return nil
	}
	out := make([]model.Rat, 0, len(bounds))
	seen := make(map[string]bool, len(bounds))
	for _, b := range bounds {
		k := b.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, b)
	}
	return out
}

// FormatTimestamp renders an exact instant as HH:MM:SS,mmm. It reports false
// when the instant is not a whole number of milliseconds, because such a value
// cannot be written in SRT/WebVTT syntax and would not round-trip.
func FormatTimestamp(t model.Rat) (string, bool) {
	ms, err := t.Mul(model.RatFromInt(1000)).Quo(model.RatFromInt(1))
	if err != nil {
		return "", false
	}
	n, ok := ms.Int64()
	if !ok || n < 0 {
		return "", false
	}
	h := n / 3_600_000
	n %= 3_600_000
	m := n / 60_000
	n %= 60_000
	s := n / 1000
	f := n % 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, f), true
}
