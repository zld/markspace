// Package subtitle parses SRT and WebVTT cue files onto the shared nanosecond
// timeline while preserving every source line and byte offset.
//
// Parsing is deliberately recovering: a structurally broken cue is reported as a
// stable model.Error and scanning resumes at the next plausible cue header, so
// the caller can still recover the surrounding content.
package subtitle

import (
	"strconv"
	"strings"

	"mediarc/model"
	"mediarc/timebase"
)

// parseSRTBlock converts one block into a cue, or reports why it cannot.
func parseSRTBlock(b block) (Cue, *model.Error) {
	bad := func(detail string) *model.Error {
		return model.New(model.CodeInvalidCue, stream, detail).WithOffset(b.startOffset())
	}
	timing := b.timingIndex()
	if timing < 0 {
		return Cue{}, bad("cue block has no timing line")
	}
	index := 0
	if timing == 1 {
		v, ok := parseIntField(b.lines[0].text)
		if !ok {
			return Cue{}, bad("cue index line is not a non-negative integer")
		}
		index = v
	} else if timing > 1 {
		return Cue{}, bad("cue block has more than one line before the timing line")
	}
	startText, endText, settings, ok := splitTiming(b.lines[timing].text)
	if !ok {
		return Cue{}, bad("malformed timing line")
	}
	start, err := parseSRTClock(startText)
	if err != nil {
		return Cue{}, err
	}
	end, err := parseSRTClock(endText)
	if err != nil {
		return Cue{}, err
	}
	if end <= start {
		return Cue{}, bad("cue end is not after its start")
	}
	cue := Cue{
		Index:       index,
		Start:       start,
		End:         end,
		Settings:    settings,
		StartOffset: b.startOffset(),
		EndOffset:   b.endOffset(),
		HeaderLine:  b.lines[timing].number,
	}
	for _, line := range b.lines[timing+1:] {
		cue.Lines = append(cue.Lines, Line{Number: line.number, Offset: line.offset, Text: line.text})
	}
	return cue, nil
}

// parseSRTClock parses an SRT timestamp, which must carry hours as well as the
// fractional part.
func parseSRTClock(text string) (int64, *model.Error) {
	normalized := strings.ReplaceAll(text, ",", ".")
	if strings.Count(normalized, ":") != 2 || !strings.Contains(normalized, ".") {
		return 0, model.New(model.CodeInvalidCue, stream,
			"timestamp "+strconv.Quote(text)+" is not HH:MM:SS,mmm")
	}
	ns, err := timebase.ParseClock(text)
	if err != nil {
		return 0, model.New(model.CodeInvalidCue, stream,
			"timestamp "+strconv.Quote(text)+" is not a valid clock").WithCause(err)
	}
	return ns, nil
}

// ParseSRT parses a SubRip document, recovering around broken cues.
func ParseSRT(data []byte) Result {
	result := Result{Format: FormatSRT}
	for _, b := range scanBlocks(splitLines(data), false) {
		cue, err := parseSRTBlock(b)
		if err != nil {
			result.Issues = append(result.Issues, err)
			continue
		}
		result.Cues = append(result.Cues, cue)
	}
	result.Checksum = checksumCues(result.Cues)
	return result
}

// Detect guesses the format from the first non-empty line. It defaults to SRT.
func Detect(data []byte) Format {
	for _, line := range splitLines(data) {
		trimmed := strings.TrimSpace(line.text)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "WEBVTT") {
			return FormatVTT
		}
		return FormatSRT
	}
	return FormatSRT
}
