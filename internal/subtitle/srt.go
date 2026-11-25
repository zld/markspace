// Package subtitle parses SRT and WebVTT while retaining source digests and
// exact cue boundaries.
package subtitle

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"

	"mediapack/internal/clock"
	"mediapack/internal/fault"
)

// Cue is a half-open [Start, End) subtitle interval in the shared timebase.
type Cue struct {
	Index int         `json:"index"`
	ID    string      `json:"id,omitempty"`
	Start clock.Ticks `json:"start"`
	End   clock.Ticks `json:"end"`
	Text  string      `json:"text"`
	Line  int         `json:"line"`
}

// Document retains the exact source digest alongside parsed cues.
type Document struct {
	Format    string `json:"format"`
	Source    string `json:"source"`
	RawSHA256 string `json:"raw_sha256"`
	CueCount  int    `json:"cue_count"`
	Cues      []Cue  `json:"cues"`
}

// Digest returns the 64-character lowercase SHA-256 digest of raw.
func Digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

var stampRe = regexp.MustCompile(`^(\d{1,2}):(\d{2}):(\d{2})([.,])(\d{1,3})$`)

// ParseStamp parses HH:MM:SS,mmm or HH:MM:SS.mmm into shared ticks.
func ParseStamp(source string, offset int64, stamp string) (clock.Ticks, error) {
	m := stampRe.FindStringSubmatch(stamp)
	if m == nil {
		return 0, fault.At(fault.SubtitleParse, source, offset, "invalid timestamp %q", stamp)
	}
	h, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	sec, _ := strconv.Atoi(m[3])
	msText := m[5]
	for len(msText) < 3 {
		msText += "0"
	}
	ms, _ := strconv.Atoi(msText[:3])
	if min > 59 || sec > 59 {
		return 0, fault.At(fault.OutOfRange, source, offset, "timestamp field out of range %q", stamp)
	}
	t := clock.Ticks((((int64(h)*3600 + int64(min)*60 + int64(sec)) * 1000) + int64(ms)) *
		clock.TicksPerSecond / 1000)
	if err := clock.CheckNonNegative(source, offset, t); err != nil {
		return 0, err
	}
	return t, nil
}

type block struct {
	startLine   int
	startOffset int
	lines       []string
	lineStarts  []int
}

func splitBlocks(raw string) []block {
	lines := strings.Split(raw, "\n")
	var blocks []block
	var cur block
	offset := 0
	flush := func() {
		if len(cur.lines) > 0 {
			blocks = append(blocks, cur)
		}
		cur = block{}
	}
	for i, rawLine := range lines {
		lineStart := offset
		line := strings.TrimSuffix(rawLine, "\r") // retain original offsets for CRLF
		if strings.TrimSpace(line) == "" {
			flush()
			offset = lineStart + len(rawLine) + 1
			continue
		}
		if len(cur.lines) == 0 {
			cur.startLine = i + 1
			cur.startOffset = lineStart
		}
		cur.lines = append(cur.lines, line)
		cur.lineStarts = append(cur.lineStarts, lineStart)
		offset = lineStart + len(rawLine) + 1
	}
	flush()
	return blocks
}

func blockLineOffset(b block, idx int) int {
	if idx < 0 || idx >= len(b.lineStarts) {
		return 0
	}
	return b.lineStarts[idx] - b.startOffset
}

func parseTimingLine(source string, b block, line string, lineOffset int, format string) (clock.Ticks, clock.Ticks, int, error) {
	arrow := strings.Index(line, "-->")
	if arrow < 0 {
		return 0, 0, 0, fault.At(fault.SubtitleParse, source, int64(b.startOffset+lineOffset),
			"%s cue missing '-->' timing line", format)
	}
	left := strings.TrimSpace(line[:arrow])
	rest := strings.TrimSpace(line[arrow+3:])
	rightFields := strings.Fields(rest)
	if left == "" || len(rightFields) == 0 {
		return 0, 0, 0, fault.At(fault.SubtitleParse, source, int64(b.startOffset+lineOffset+arrow),
			"%s cue has incomplete timing", format)
	}
	leftPos := strings.Index(line, left)
	start, err := ParseStamp(source, int64(b.startOffset+lineOffset+leftPos), left)
	if err != nil {
		return 0, 0, 0, err
	}
	endPos := arrow + 3 + strings.Index(line[arrow+3:], rightFields[0])
	end, err := ParseStamp(source, int64(b.startOffset+lineOffset+endPos), rightFields[0])
	if err != nil {
		return 0, 0, 0, err
	}
	return start, end, arrow, nil
}

// ValidateCues classifies zero-duration and overlapping cues without changing
// or silently repairing them. Cues are expected in source order.
func ValidateCues(source string, cues []Cue) error {
	for i, c := range cues {
		if c.End == c.Start {
			return fault.At(fault.ZeroDurationCue, source, int64(c.Start),
				"cue %d has zero duration at %d ticks", i+1, c.Start)
		}
		if c.End < c.Start {
			return fault.At(fault.NegativeTime, source, int64(c.Start),
				"cue %d ends before it starts", i+1)
		}
		if i > 0 && c.Start < cues[i-1].End {
			return fault.At(fault.OverlappingCue, source, int64(c.Start),
				"cue %d overlaps previous cue end %d", i+1, cues[i-1].End)
		}
	}
	return nil
}

// ParseSRT parses SubRip data. It retains the raw SHA-256 digest and exact
// half-open cue boundaries.
func ParseSRT(source string, raw []byte) (*Document, error) {
	text := string(raw)
	doc := &Document{Format: "srt", Source: source, RawSHA256: Digest(raw)}
	for _, b := range splitBlocks(text) {
		if len(b.lines) < 2 {
			return nil, fault.At(fault.SubtitleParse, source, int64(b.startOffset),
				"SRT block at line %d is too short", b.startLine)
		}
		lineIdx := 0
		index := len(doc.Cues) + 1
		if n, err := strconv.Atoi(strings.TrimSpace(b.lines[0])); err == nil {
			index = n
			lineIdx = 1
		}
		if lineIdx >= len(b.lines) || !strings.Contains(b.lines[lineIdx], "-->") {
			return nil, fault.At(fault.SubtitleParse, source, int64(b.startOffset),
				"SRT cue %d lacks timing line", index)
		}
		start, end, _, err := parseTimingLine(source, b, b.lines[lineIdx], blockLineOffset(b, lineIdx), "SRT")
		if err != nil {
			return nil, err
		}
		textLines := b.lines[lineIdx+1:]
		if len(textLines) == 0 {
			return nil, fault.At(fault.SubtitleParse, source, int64(b.startOffset),
				"SRT cue %d has no text", index)
		}
		doc.Cues = append(doc.Cues, Cue{
			Index: index, Start: start, End: end,
			Text: strings.Join(textLines, "\n"), Line: b.startLine,
		})
	}
	doc.CueCount = len(doc.Cues)
	if err := ValidateCues(source, doc.Cues); err != nil {
		return nil, err
	}
	return doc, nil
}
