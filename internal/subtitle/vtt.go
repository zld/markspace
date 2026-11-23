package subtitle

import (
	"io"
	"strings"

	"mediaverify/internal/errs"
)

// ParseWebVTT parses a WebVTT track. The WEBVTT header and NOTE/STYLE/REGION
// blocks are skipped; cue identifiers (non-timing lines preceding the timing
// line) are accepted. Timestamps use '.' separators and may omit the hour
// component. Cue settings after the end clock are allowed.
func ParseWebVTT(r io.Reader, cfg Config) ([]Cue, error) {
	lines, err := readLines(r)
	if err != nil {
		return nil, errs.New(errs.KindInvalidInput, "subtitle", "read error: %v", err)
	}
	c := &counter{cfg: cfg}
	var cues []Cue
	idx := 0
	i := 0
	// Header: first non-blank line must begin with WEBVTT.
	for i < len(lines) && lines[i].text == "" {
		i++
	}
	if i >= len(lines) || !strings.HasPrefix(lines[i].text, "WEBVTT") {
		return nil, errs.New(errs.KindInvalidInput, "subtitle",
			"line %d: missing WEBVTT header", minLine(i, lines))
	}
	i++
	for i < len(lines) {
		if lines[i].text == "" {
			i++
			continue
		}
		// Skip block-level metadata.
		if isControlBlock(lines[i].text) {
			for i < len(lines) && lines[i].text != "" {
				i++
			}
			continue
		}
		firstRow := lines[i].line
		timing := lines[i].text
		if !strings.Contains(timing, "-->") {
			// Cue identifier line: the next non-blank line must be timing.
			i++
			if i >= len(lines) || !strings.Contains(lines[i].text, "-->") {
				return nil, errs.New(errs.KindInvalidInput, "subtitle",
					"line %d: cue identifier not followed by timing", firstRow)
			}
			timing = lines[i].text
		}
		start, end, terr := splitTimestamps(timing, "-->")
		if terr != nil {
			return nil, errs.New(errs.Class(terr), "subtitle", "line %d: %v", lines[i].line, terr)
		}
		idx++
		i++
		var body []string
		for i < len(lines) && lines[i].text != "" {
			if isControlBlock(lines[i].text) {
				break
			}
			body = append(body, lines[i].text)
			i++
		}
		if err := finalize(&cues, idx, firstRow, start, end, body, c); err != nil {
			return nil, err
		}
	}
	if err := validateOrder(cues); err != nil {
		return nil, err
	}
	return cues, nil
}

func isControlBlock(s string) bool {
	return strings.HasPrefix(s, "NOTE") ||
		strings.HasPrefix(s, "STYLE") ||
		strings.HasPrefix(s, "REGION")
}

func minLine(i int, lines []numbered) int {
	if i < len(lines) {
		return lines[i].line
	}
	return 0
}
