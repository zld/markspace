package subtitle

import (
	"io"
	"strconv"
	"strings"

	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
)

// ParseSRT parses a SubRip track. Blocks are separated by blank lines; each
// block is an optional sequence number, a timing line (" --> ") and one or
// more payload lines. Sequence numbers must be strictly increasing.
func ParseSRT(r io.Reader, cfg Config) ([]Cue, error) {
	lines, err := readLines(r)
	if err != nil {
		return nil, errs.New(errs.KindInvalidInput, "subtitle", "read error: %v", err)
	}
	c := &counter{cfg: cfg}
	var cues []Cue
	idx := 0
	for i := 0; i < len(lines); {
		if lines[i].text == "" {
			i++
			continue
		}
		firstRow := lines[i].line
		timing := lines[i].text
		// Optional sequence number line.
		if !strings.Contains(timing, "-->") {
			n, perr := strconv.Atoi(timing)
			if perr != nil {
				return nil, errs.New(errs.KindInvalidInput, "subtitle",
					"line %d: expected sequence number or timing, got %q", firstRow, timing)
			}
			idx++
			if n != idx {
				return nil, errs.New(errs.KindInvalidInput, "subtitle",
					"line %d: sequence number %d, want %d", firstRow, n, idx)
			}
			i++
			if i >= len(lines) || lines[i].text == "" {
				return nil, errs.New(errs.KindInvalidInput, "subtitle",
					"line %d: cue %d missing timing line", firstRow, idx)
			}
			timing = lines[i].text
		} else {
			idx++
		}
		if !strings.Contains(timing, "-->") {
			return nil, errs.New(errs.KindInvalidInput, "subtitle",
				"line %d: missing timing arrow", lines[i].line)
		}
		start, end, terr := splitTimestamps(timing, "-->")
		if terr != nil {
			return nil, errs.New(errs.Class(terr), "subtitle", "line %d: %v", lines[i].line, terr)
		}
		i++
		var body []string
		for i < len(lines) && lines[i].text != "" {
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

// validateOrder enforces non-decreasing start times and no overlaps.
func validateOrder(cues []Cue) error {
	var prev clock.Ticks
	for i, cue := range cues {
		if cue.Start < prev && i > 0 {
			return errs.New(errs.KindTimestamp, "subtitle",
				"line %d: cue starts before previous cue", cue.FirstRow)
		}
		prev = cue.Start
	}
	return nil
}
