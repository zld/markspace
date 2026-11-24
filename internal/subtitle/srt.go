// Package subtitle parses SRT and WebVTT cues, preserving original byte
// offsets so every diagnostic can name a line and column.
package subtitle

import (
	"strings"

	"mediaaudit/internal/model"
)

// Format identifies the source syntax.
type Format string

const (
	FormatSRT Format = "srt"
	FormatVTT Format = "vtt"
)

// Cue is one parsed subtitle block. Start/End are exact media-clock instants
// after applying any document offset. StartText/EndText keep the original
// boundary tokens for auditing.
type Cue struct {
	Index     int
	Start     model.Rat
	End       model.Rat
	StartText string
	EndText   string
	Lines     []string
	Pos       model.InputPos
}

// Text returns the cue body with newlines joined.
func (c Cue) Text() string { return strings.Join(c.Lines, "\n") }

// Span returns the cue interval.
func (c Cue) Span() model.Span { return model.Span{Start: c.Start, End: c.End} }

// Document is the parse result for one subtitle file.
type Document struct {
	Format Format
	Cues   []Cue
	// MPEGTSOffset is the WebVTT X-TIMESTAMP-MAP offset applied to cues.
	MPEGTSOffset model.Rat
	Errors       model.Errors
}

// Options tune parsing. Offset shifts every cue timestamp onto the unified
// clock (added, not subtracted) and defaults to zero.
type Options struct {
	Offset model.Rat
}

// Parse parses a complete subtitle document. Recoverable cue problems are
// returned as diagnostics and the remaining cues are still usable.
func Parse(data []byte, file string, format Format, opts Options) Document {
	doc := Document{Format: format}
	switch format {
	case FormatSRT:
		doc.Cues, doc.Errors = parseSRT(data, file)
	case FormatVTT:
		h, herr := parseHeader(data, file)
		if herr != nil {
			doc.Errors.Add(herr)
		}
		doc.MPEGTSOffset = h.offset
		cues, cuesErr := parseVTT(data, file)
		doc.Cues = cues
		for _, e := range cuesErr {
			doc.Errors.Add(e)
		}
	case "":
		doc.Errors.Add(model.NewError(model.CodeInvalidArgument, "subtitle", "",
			model.InputPos{File: file}, "subtitle format is required"))
		return doc
	default:
		doc.Errors.Add(model.NewError(model.CodeInvalidArgument, "subtitle", "",
			model.InputPos{File: file}, "unsupported subtitle format %q", format))
		return doc
	}
	shift := opts.Offset
	if !doc.MPEGTSOffset.IsZero() {
		shift = shift.Add(doc.MPEGTSOffset)
	}
	if !shift.IsZero() {
		for i := range doc.Cues {
			doc.Cues[i].Start = doc.Cues[i].Start.Add(shift)
			doc.Cues[i].End = doc.Cues[i].End.Add(shift)
		}
	}
	return doc
}

// block is one blank-line separated chunk of a subtitle file.
type block struct {
	file      string
	startLine int
	lines     []string
}

// splitBlocks splits on blank lines. CRLF is normalised and a leading BOM is
// dropped. Line numbers are 1-based over the original document.
func splitBlocks(data []byte, file string) []block {
	text := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.Split(text, "\n")
	blocks := make([]block, 0, 8)
	cur := block{file: file}
	for i, raw := range lines {
		raw = strings.TrimSuffix(raw, "\r")
		if strings.TrimSpace(raw) == "" {
			if len(cur.lines) > 0 {
				blocks = append(blocks, cur)
				cur = block{file: file}
			}
			continue
		}
		if len(cur.lines) == 0 {
			cur.startLine = i + 1
		}
		cur.lines = append(cur.lines, raw)
	}
	if len(cur.lines) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}

// timestampLine holds a validated timing line and the columns of its tokens.
type timestampLine struct {
	start, end model.Rat
	startText  string
	endText    string
	startCol   int
	endCol     int
}

// findArrow returns the index of "-->" in a line.
func findArrow(line string) int { return strings.Index(line, "-->") }

// timingLine parses the timing line of an SRT/VTT cue.
func timingLine(line string, lineNo int, file string, vtt bool) (timestampLine, *model.Error) {
	var out timestampLine
	pos := model.InputPos{File: file, Line: lineNo}
	idx := findArrow(line)
	if idx < 0 {
		return out, model.NewError(model.CodeSubtitleMalformed, "subtitle", "", pos,
			"cue is missing the --> separator in %q", line)
	}
	left := line[:idx]
	right := line[idx+3:]
	rightTrim := strings.TrimSpace(right)
	if rightTrim == "" {
		return out, model.NewError(model.CodeSubtitleMalformed, "subtitle", "", pos,
			"cue has no end timestamp")
	}
	endTok, trailing, _ := strings.Cut(rightTrim, " ")
	if trailing != "" && !vtt {
		return out, model.NewError(model.CodeSubtitleMalformed, "subtitle", "", pos,
			"unexpected text %q after the end timestamp", strings.TrimSpace(trailing))
	}
	out.startCol = len(left) - len(strings.TrimLeft(left, " \t")) + 1
	out.endCol = idx + 4 + (len(right) - len(strings.TrimLeft(right, " \t")))
	startT, startText, err := parseTimestamp(left, out.startCol, pos, vtt)
	if err != nil {
		return out, err
	}
	endT, endText, err := parseTimestamp(endTok, out.endCol, pos, vtt)
	if err != nil {
		return out, err
	}
	out.start, out.end = startT, endT
	out.startText, out.endText = startText, endText
	return out, nil
}

// buildCue validates ordering and fills a Cue.
func buildCue(idx int, tl timestampLine, body []string, pos model.InputPos) (Cue, *model.Error) {
	c := Cue{Index: idx, Start: tl.start, End: tl.end,
		StartText: tl.startText, EndText: tl.endText, Lines: body, Pos: pos}
	switch {
	case tl.end.Equal(tl.start):
		return c, model.NewError(model.CodeSubtitleZero, "subtitle", "", pos,
			"cue %d has zero duration (%s --> %s)", idx, tl.startText, tl.endText)
	case tl.end.Cmp(tl.start) < 0:
		return c, model.NewError(model.CodeSubtitleReversed, "subtitle", "", pos,
			"cue %d ends before it starts (%s --> %s)", idx, tl.startText, tl.endText)
	}
	return c, nil
}

// parseSRT parses an SRT document. The numeric index line is optional but, if
// present, must be a positive integer.
func parseSRT(data []byte, file string) ([]Cue, model.Errors) {
	var es model.Errors
	var cues []Cue
	for _, b := range splitBlocks(data, file) {
		idx := len(cues) + 1
		start := 0
		if findArrow(b.lines[0]) < 0 {
			label := strings.TrimSpace(b.lines[0])
			n, ok := parsePositiveInt(label)
			if !ok {
				es.Add(model.NewError(model.CodeSubtitleMalformed, "subtitle", "", model.InputPos{
					File: file, Line: b.startLine}, "expected a numeric cue index, got %q", label))
				continue
			}
			idx = n
			start = 1
		}
		if start >= len(b.lines) {
			es.Add(model.NewError(model.CodeSubtitleMalformed, "subtitle", "", model.InputPos{
				File: file, Line: b.startLine}, "cue %d has no timing line", idx))
			continue
		}
		tl, err := timingLine(b.lines[start], b.startLine+start, file, false)
		if err != nil {
			es.Add(err)
			continue
		}
		cue, cerr := buildCue(idx, tl, b.lines[start+1:], model.InputPos{File: file, Line: b.startLine + start})
		if cerr != nil {
			es.Add(cerr)
			continue
		}
		cues = append(cues, cue)
	}
	return cues, es
}

func parsePositiveInt(s string) (int, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}
