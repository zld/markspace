// Package check re-parses serialized output and compares it against the
// pre-serialization cues, detecting missing entries, duplicates, text drift
// and per-cue timing deviation.
package check

import (
	"fmt"
	"strings"
	"time"

	"subsync/internal/model"
)

// Issue kinds surfaced to the terminal and JSONL consumers.
const (
	IssueMissing   = "missing"
	IssueDuplicate = "duplicate"
	IssueText      = "text_changed"
)

// CueReport is the verification outcome for one original cue.
type CueReport struct {
	Cue       model.Cue
	Delta     time.Duration // max of |start diff|, |end diff|; 0 when unmatched
	Matched   bool
	Issue     string // empty when fully matched
	Duplicate bool   // true when more than one output cue mapped here
}

// Report is the full round-trip verdict.
type Report struct {
	PerCue     []CueReport
	Missing    int
	Duplicates int
	TextChange int
	// MaxDelta is the largest per-cue timing deviation observed.
	MaxDelta time.Duration
}

// Verify pairs original cues with the cues recovered from serialized output.
// Matching prefers cue id within the same track; ids that collide or are
// absent fall back to normalized text plus a duplicate sequence number.
func Verify(original, recovered []model.Cue) *Report {
	rep := &Report{PerCue: make([]CueReport, len(original))}

	trackCues := map[string][]model.Cue{}
	for _, c := range recovered {
		trackCues[c.Track] = append(trackCues[c.Track], c)
	}
	consumed := map[string][]bool{}
	for track, pool := range trackCues {
		consumed[track] = make([]bool, len(pool))
	}
	// idHits counts how many pool entries share each non-empty id, so genuine
	// duplicates are detected without flagging id-less cues.
	idHits := map[string]int{}
	for track, pool := range trackCues {
		for _, c := range pool {
			if c.ID != "" {
				idHits[track+"\x00"+c.ID]++
			}
		}
	}
	textSeq := map[string]int{}

	for i, cue := range original {
		cr := CueReport{Cue: cue}
		pool := trackCues[cue.Track]
		idx := -1

		// ID matching only applies when the source actually carried an id;
		// id-less cues always use the normalized-text fallback so generated
		// serializer sequence numbers cannot hijack the match.
		if cue.ID != "" {
			idx = takeByID(pool, consumed[cue.Track], cue.ID)
		}
		if idx < 0 {
			// Each original cue with the same normalized text consumes the
			// next available output cue carrying that text; the sequence is
			// claimed up front so duplicate originals each get their own.
			key := cue.Track + "\x00" + NormalizeText(cue.Text)
			seq := textSeq[key]
			textSeq[key]++
			idx = takeByText(pool, consumed[cue.Track], cue.Text, seq)
		}
		if idx < 0 {
			cr.Issue = IssueMissing
			rep.Missing++
			rep.PerCue[i] = cr
			continue
		}

		match := pool[idx]
		consumed[cue.Track][idx] = true
		cr.Matched = true
		cr.Delta = maxDelta(cue, match)
		if cr.Delta > rep.MaxDelta {
			rep.MaxDelta = cr.Delta
		}
		if NormalizeText(match.Text) != NormalizeText(cue.Text) {
			cr.Issue = IssueText
			rep.TextChange++
		}
		if idHits[cue.Track+"\x00"+cue.ID] > 1 {
			cr.Duplicate = true
			if cr.Issue == "" {
				cr.Issue = IssueDuplicate
			}
			rep.Duplicates++
		}
		rep.PerCue[i] = cr
	}
	return rep
}

// takeByID returns the first unconsumed pool index carrying id, or -1.
func takeByID(pool []model.Cue, used []bool, id string) int {
	for i := range pool {
		if !used[i] && pool[i].ID == id {
			return i
		}
	}
	return -1
}

// takeByText returns the nth (0-based) unconsumed cue with matching
// normalized text, giving each duplicate its own distinct output cue. Entries
// already claimed by id matching are skipped via the used bitmap rather than
// by id presence, since serializers assign sequence ids to every cue.
func takeByText(pool []model.Cue, used []bool, want string, seq int) int {
	norm := NormalizeText(want)
	seen := 0
	for i := range pool {
		if NormalizeText(pool[i].Text) != norm {
			continue
		}
		// Count every matching position, including ones already consumed, so
		// that seq addresses the same slot the caller numbered.
		if seen == seq {
			if used[i] {
				return -1
			}
			return i
		}
		seen++
	}
	return -1
}

// NormalizeText canonicalizes line endings and trims outer whitespace so that
// purely cosmetic differences do not count as text changes.
func NormalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

func maxDelta(a, b model.Cue) time.Duration {
	d1 := absDur(a.Start - b.Start)
	d2 := absDur(a.End - b.End)
	if d1 > d2 {
		return d1
	}
	return d2
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// String renders a compact human-readable verdict for terminal output.
func (r *Report) String() string {
	return fmt.Sprintf("roundtrip: max_delta=%s missing=%d duplicates=%d text_changes=%d",
		r.MaxDelta, r.Missing, r.Duplicates, r.TextChange)
}
