// Package subtitle parses SRT and WebVTT streams. Parsing is streaming and
// every cue and defect carries the raw byte offset of its source line so the
// report and rollback layers can point back into the original file.
package subtitle

import (
	"bufio"
	"errors"
	"io"
	"strings"

	"packcheck/internal/clock"
	"packcheck/internal/model"
)

// Cue is one subtitle interval on the unified time base.
type Cue struct {
	Index       int
	Start       clock.Rat
	End         clock.Rat
	Text        string
	StartOffset int64 // byte offset of the cue header line
	EndOffset   int64 // byte offset immediately after the cue payload
	Empty       bool
	AtCutPoint  bool
}

// CutPoints are normalized instants where container cuts occur.
type CutPoints []clock.Rat

// Contains reports whether t coincides with a declared cut point.
func (c CutPoints) Contains(t clock.Rat) bool {
	for _, p := range c {
		if p.Cmp(t) == 0 {
			return true
		}
	}
	return false
}

// ParseResult is the output of any subtitle parser.
type ParseResult struct {
	Format string
	Rate   clock.Rate
	Cues   []Cue
	Issues model.IssueLog
	Bytes  int64
}

// offsetReader returns lines together with the absolute byte offset at which
// each line starts. It handles CRLF without hiding the raw offsets.
type offsetReader struct {
	r   *bufio.Reader
	pos int64
}

func newOffsetReader(r io.Reader) *offsetReader {
	return &offsetReader{r: bufio.NewReaderSize(r, 4096)}
}

// ReadLine returns the trimmed line, its starting offset and ok=false at EOF.
func (o *offsetReader) ReadLine() (line string, offset int64, ok bool) {
	for {
		offset = o.pos
		raw, err := o.r.ReadString('\n')
		o.pos += int64(len(raw))
		line = strings.TrimRight(raw, "\r\n")
		if errors.Is(err, io.EOF) {
			if line == "" {
				return "", 0, false
			}
			return line, offset, true
		}
		if err != nil {
			return "", 0, false
		}
		return line, offset, true
	}
}

// validateCue applies the cross-cue rules shared by both formats.
func validateCue(prev *Cue, cue *Cue, cuts CutPoints, issues *model.IssueLog, format string) {
	if cue.End.Cmp(cue.Start) < 0 {
		issues.Add(model.Issue{
			Code: model.CodeInvertedCue, Severity: model.SeverityError,
			Layer: model.LayerSubtitle, ByteOffset: cue.StartOffset,
			Message: format + " cue end precedes start",
		})
		return
	}
	if strings.TrimSpace(cue.Text) == "" {
		cue.Empty = true
		issues.Add(model.Issue{
			Code: model.CodeEmptyCue, Severity: model.SeverityWarning,
			Layer: model.LayerSubtitle, ByteOffset: cue.StartOffset,
			Message: format + " cue has empty payload",
		})
	}
	if prev != nil && cue.Start.Cmp(prev.End) < 0 {
		issues.Add(model.Issue{
			Code: model.CodeOverlappingCue, Severity: model.SeverityWarning,
			Layer: model.LayerSubtitle, ByteOffset: cue.StartOffset,
			Message: format + " cue overlaps previous cue",
		})
	}
	if cuts.Contains(cue.Start) || cuts.Contains(cue.End) {
		cue.AtCutPoint = true
		issues.Add(model.Issue{
			Code: model.CodeCueAtCutPoint, Severity: model.SeverityWarning,
			Layer: model.LayerSubtitle, ByteOffset: cue.StartOffset,
			Message: format + " cue boundary lands on a container cut point",
		})
	}
}
