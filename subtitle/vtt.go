// Package subtitle parses SRT and WebVTT cue files onto the shared nanosecond
// timeline while preserving every source line and byte offset.
//
// Parsing is deliberately recovering: a structurally broken cue is reported as a
// stable model.Error and scanning resumes at the next plausible cue header, so
// the caller can still recover the surrounding content.
package subtitle

import (
	"fmt"
	"strings"

	"mediarc/model"
	"mediarc/timebase"
)

// vttHeader holds document-level header data.
type vttHeader struct {
	// Present is true when the file begins with the WEBVTT magic line.
	Present bool
	// Line is the verbatim "WEBVTT ..." line.
	Line sourceLine
}

// ParseWebVTT parses a WebVTT document. The BOM, if any, is skipped.
func ParseWebVTT(data []byte) Result {
	result := Result{Format: FormatVTT}
	lines := splitLines(data)
	header := vttHeader{}
	start := 0
	for i, line := range lines {
		if strings.TrimSpace(line.text) == "" {
			start = i + 1
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line.text), "WEBVTT") {
			header.Present = true
			header.Line = line
			start = i + 1
			// The header block runs until the first blank line and may carry
			// metadata such as Kind: or Language:.
			for start < len(lines) && strings.TrimSpace(lines[start].text) != "" {
				start++
			}
		}
		break
	}
	if !header.Present {
		result.Issues = append(result.Issues, model.New(model.CodeInvalidCue, stream,
			"missing WEBVTT header").WithOffset(0))
	}

	for _, b := range scanBlocks(lines[start:], true) {
		if isVTTMetaBlock(b) {
			continue
		}
		cue, err := parseVTTBlock(b)
		if err != nil {
			result.Issues = append(result.Issues, err)
			continue
		}
		result.Cues = append(result.Cues, cue)
	}
	result.Checksum = checksumCues(result.Cues)
	return result
}

func isVTTMetaBlock(b block) bool {
	if len(b.lines) == 0 {
		return false
	}
	first := strings.TrimSpace(b.lines[0].text)
	for _, prefix := range []string{"NOTE", "STYLE", "REGION"} {
		if first == prefix || strings.HasPrefix(first, prefix+" ") || strings.HasPrefix(first, prefix+"\t") {
			return true
		}
	}
	return false
}

// parseVTTBlock converts one WebVTT cue block.
func parseVTTBlock(b block) (Cue, *model.Error) {
	bad := func(detail string) *model.Error {
		return model.New(model.CodeInvalidCue, stream, detail).WithOffset(b.startOffset())
	}
	timing := b.timingIndex()
	if timing < 0 {
		return Cue{}, bad("cue block has no timing line")
	}
	if timing > 1 {
		return Cue{}, bad("cue block has more than one line before the timing line")
	}
	cue := Cue{
		StartOffset: b.startOffset(),
		EndOffset:   b.endOffset(),
		HeaderLine:  b.lines[timing].number,
	}
	if timing == 1 {
		identifier := strings.TrimSpace(b.lines[0].text)
		if strings.Contains(identifier, "-->") {
			return Cue{}, bad("cue identifier contains a timing arrow")
		}
		if v, ok := parseIntField(identifier); ok {
			cue.Index = v
		}
	}
	startText, endText, settings, ok := splitTiming(b.lines[timing].text)
	if !ok {
		return Cue{}, bad("malformed timing line")
	}
	start, err := timebase.ParseClock(startText)
	if err != nil {
		return Cue{}, vttClockError(startText, err)
	}
	end, err := timebase.ParseClock(endText)
	if err != nil {
		return Cue{}, vttClockError(endText, err)
	}
	if end <= start {
		return Cue{}, bad("cue end is not after its start")
	}
	if err := validateVTTSettings(settings); err != nil {
		return Cue{}, bad(err.Error())
	}
	cue.Start = start
	cue.End = end
	cue.Settings = settings
	for _, line := range b.lines[timing+1:] {
		cue.Lines = append(cue.Lines, Line{Number: line.number, Offset: line.offset, Text: line.text})
	}
	return cue, nil
}

func vttClockError(text string, err error) *model.Error {
	return model.New(model.CodeInvalidCue, stream,
		fmt.Sprintf("timestamp %q is not a valid WebVTT clock", text)).WithCause(err)
}

// validateVTTSettings checks the shape of cue settings. Unknown setting names
// are tolerated, but a token without a ':' is not a setting at all.
func validateVTTSettings(settings string) error {
	fields := strings.Fields(settings)
	for _, field := range fields {
		name, value, ok := strings.Cut(field, ":")
		if !ok || name == "" || value == "" {
			return fmt.Errorf("cue setting %q is not name:value", field)
		}
		switch name {
		case "vertical":
			if value != "rl" && value != "lr" {
				return fmt.Errorf("vertical setting %q is not rl or lr", value)
			}
		case "line", "position":
			// Accept bare percentages and optional alignment suffixes; the
			// exact grammar is validated by the renderer, not the parser.
		case "align":
			switch value {
			case "start", "center", "end", "left", "right":
			default:
				return fmt.Errorf("align setting %q is not a known alignment", value)
			}
		}
	}
	return nil
}
