// Package subtitle parses SRT and WebVTT cues into the shared tick time base.
// Invalid times, negative durations and overlaps are reported with their
// 1-based source line and are never silently corrected.
package subtitle

import (
	"fmt"
	"strings"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
)

// Cue is one subtitle cue on the shared 90 kHz base.
type Cue struct {
	Index   int
	Start   clock.Tick
	End     clock.Tick
	Text    string
	StartLn int
	EndLn   int
}

// Duration is the cue length in ticks.
func (c Cue) Duration() clock.Tick { return c.End - c.Start }

// Track is the parse result for one subtitle input.
type Track struct {
	Input string
	Kind  string
	Cues  []Cue
}

// Line locator helpers keep all errors in one place.
func lineIssue(cat issue.Category, input string, line int, format string, args ...any) issue.Issue {
	return issue.New(cat, input, fmt.Sprintf(format, args...)).WithLine(line)
}

func splitLines(src string) []string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")
	src = strings.TrimPrefix(src, "\ufeff")
	return strings.Split(src, "\n")
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

func parseIntStrict(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return 0, false
		}
	}
	return n, true
}

// splitSRTTiming splits "start --> end" plus any trailing settings.
func splitTiming(line string, sep string) (string, string, bool) {
	idx := strings.Index(line, sep)
	if idx < 0 {
		return "", "", false
	}
	left := strings.TrimSpace(line[:idx])
	rest := strings.TrimSpace(line[idx+len(sep):])
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", "", false
	}
	return left, fields[0], true
}

func effective(t clock.Tick, base clock.Tick) clock.Tick {
	if t < base {
		return 0
	}
	return t - base
}

// overlapIssues reports each cue whose start precedes the prior cue's end,
// citing both source lines.
func overlapIssues(input string, cues []Cue) issue.List {
	var out issue.List
	for i := 1; i < len(cues); i++ {
		prev, cur := cues[i-1], cues[i]
		if cur.Start < prev.End {
			out.Add(issue.New(issue.OverlappingCue, input,
				fmt.Sprintf("cue %d starts %s before cue %d ends %s (at %s)",
					cur.Index, cur.Start, prev.Index, prev.End, cur.Start)).
				WithLine(cur.StartLn).WithRef(fmt.Sprintf("cue:%d", cur.Index)))
		}
	}
	return out
}
