// Package subtitle parses SRT and WebVTT cue files onto the shared nanosecond
// timeline while preserving every source line and byte offset.
//
// Parsing is deliberately recovering: a structurally broken cue is reported as a
// stable model.Error and scanning resumes at the next plausible cue header, so
// the caller can still recover the surrounding content.
package subtitle

import (
	"strings"
)

// looksLikeTiming reports whether text carries a cue timing arrow.
func looksLikeTiming(text string) bool {
	return strings.Contains(text, "-->")
}

// splitTiming separates "start --> end [settings]" and trims the fields.
func splitTiming(text string) (start, end, settings string, ok bool) {
	left, right, found := strings.Cut(text, "-->")
	if !found {
		return "", "", "", false
	}
	start = strings.TrimSpace(left)
	fields := strings.Fields(right)
	if len(fields) == 0 {
		return "", "", "", false
	}
	end = fields[0]
	settings = strings.Join(fields[1:], " ")
	if start == "" || end == "" {
		return "", "", "", false
	}
	return start, end, settings, true
}

// scanBlocks groups source lines into cue candidates. A candidate begins at a
// timing line, or at an index line immediately followed by one; a blank line
// terminates the current candidate. Index lines are consumed together with
// their timing line so that a new cue is never split across two candidates.
//
// With allowTextIdentifier, any non-blank line directly above a timing line is
// taken as a cue identifier (WebVTT); otherwise only numeric indices qualify
// (SubRip), and stray text above a timing line stays in the previous cue.
func scanBlocks(lines []sourceLine, allowTextIdentifier bool) []block {
	blocks := make([]block, 0, len(lines)/3+1)
	current := block{}
	flush := func() {
		if len(current.lines) > 0 {
			blocks = append(blocks, current)
			current = block{}
		}
	}
	startsCue := func(i int) bool {
		if i+1 >= len(lines) || !looksLikeTiming(lines[i+1].text) {
			return false
		}
		if allowTextIdentifier {
			return true
		}
		_, ok := parseIntField(lines[i].text)
		return ok
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line.text) == "" {
			flush()
			continue
		}
		if startsCue(i) {
			flush()
			current.lines = append(current.lines, line, lines[i+1])
			i++
			continue
		}
		if looksLikeTiming(line.text) {
			flush()
		}
		current.lines = append(current.lines, line)
	}
	flush()
	return blocks
}
