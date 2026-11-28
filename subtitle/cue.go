// Package subtitle parses SRT and WebVTT cue files onto the shared nanosecond
// timeline while preserving every source line and byte offset.
//
// Parsing is deliberately recovering: a structurally broken cue is reported as a
// stable model.Error and scanning resumes at the next plausible cue header, so
// the caller can still recover the surrounding content.
package subtitle

import (
	"hash/fnv"
	"strconv"
	"strings"

	"mediarc/model"
)

// Format identifies the cue file syntax.
type Format int

const (
	// FormatSRT is SubRip.
	FormatSRT Format = iota
	// FormatVTT is WebVTT.
	FormatVTT
)

// String renders the format name used in reports.
func (f Format) String() string {
	if f == FormatVTT {
		return "vtt"
	}
	return "srt"
}

// Line is one source line of a cue, kept verbatim.
type Line struct {
	// Number is the 1-based line number in the source file.
	Number int
	// Offset is the byte offset of the first byte of the line.
	Offset int64
	// Text is the line content without its line terminator.
	Text string
}

// Cue is one recovered subtitle cue.
type Cue struct {
	// Index is the numeric cue index when the source carried one, else 0.
	Index int
	// Start and End are the cue's extent on the shared nanosecond timeline.
	Start int64
	End   int64
	// Settings holds the timing-line trailing tokens (SRT coordinates or VTT
	// cue settings), verbatim.
	Settings string
	// Lines are the subtitle payload lines with their source positions.
	Lines []Line
	// StartOffset and EndOffset delimit the cue block in the source bytes;
	// EndOffset is exclusive.
	StartOffset int64
	EndOffset   int64
	// HeaderLine is the source line number of the timing line.
	HeaderLine int
}

// Text joins the cue payload lines with "\n".
func (c Cue) Text() string {
	parts := make([]string, len(c.Lines))
	for i, line := range c.Lines {
		parts[i] = line.Text
	}
	return strings.Join(parts, "\n")
}

// Result is the outcome of a recovering parse.
type Result struct {
	Format Format
	Cues   []Cue
	// Issues lists every recovered error in source order.
	Issues []*model.Error
	// Checksum is a deterministic FNV-1a hash of the parsed cues.
	Checksum uint64
}

const stream = "subtitle"

// sourceLine is the decoded form of one input line.
type sourceLine struct {
	number int
	offset int64
	text   string
}

// splitLines decodes data into lines, accepting LF, CRLF and a leading UTF-8
// BOM. Trailing terminators are stripped and no synthetic line is emitted.
func splitLines(data []byte) []sourceLine {
	lines := make([]sourceLine, 0, len(data)/24+1)
	start := 0
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		start = 3
	}
	number := 1
	for i := start; i <= len(data); i++ {
		if i < len(data) && data[i] != '\n' {
			continue
		}
		end := i
		if end > start && data[end-1] == '\r' {
			end--
		}
		lines = append(lines, sourceLine{
			number: number,
			offset: int64(start),
			text:   string(data[start:end]),
		})
		number++
		start = i + 1
		if i == len(data) {
			break
		}
	}
	return lines
}

func parseIntField(text string) (int, bool) {
	v, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// block is a candidate cue region found by the scanner.
type block struct {
	lines []sourceLine
}

func (b block) startOffset() int64 {
	if len(b.lines) == 0 {
		return 0
	}
	return b.lines[0].offset
}

func (b block) endOffset() int64 {
	if len(b.lines) == 0 {
		return 0
	}
	last := b.lines[len(b.lines)-1]
	return last.offset + int64(len(last.text))
}

func (b block) firstLine() int {
	if len(b.lines) == 0 {
		return 0
	}
	return b.lines[0].number
}

// timingIndex is the timing line index inside the block, or -1.
func (b block) timingIndex() int {
	for i, line := range b.lines {
		if looksLikeTiming(line.text) {
			return i
		}
	}
	return -1
}

// checksumCues hashes the recovered cues deterministically.
func checksumCues(cues []Cue) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	writeInt := func(v int64) {
		u := uint64(v)
		for i := 0; i < 8; i++ {
			buf[i] = byte(u >> (8 * i))
		}
		_, _ = h.Write(buf[:])
	}
	for _, cue := range cues {
		writeInt(int64(cue.Index))
		writeInt(cue.Start)
		writeInt(cue.End)
		writeInt(int64(len(cue.Settings)))
		_, _ = h.Write([]byte(cue.Settings))
		for _, line := range cue.Lines {
			writeInt(int64(line.Number))
			writeInt(line.Offset)
			writeInt(int64(len(line.Text)))
			_, _ = h.Write([]byte(line.Text))
		}
	}
	return h.Sum64()
}
