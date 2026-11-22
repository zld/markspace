package ingest

import (
	"strings"
	"unicode/utf8"

	"mediad/internal/core"
)

// textLine is a physical input line with exact half-open byte bounds. End
// excludes the line terminator so diagnostics point at content bytes.
type textLine struct {
	Text       string
	Start      int64
	End        int64
	Next       int64 // offset of the following line
	Terminated bool  // a CRLF/LF/CR terminator was present
}

// splitLines splits raw bytes without copying line contents. CRLF, LF and a
// lone CR are accepted; the reported spans never include the terminator.
func splitLines(data []byte) []textLine {
	lines := make([]textLine, 0, 64)
	var start int64
	for i := 0; i < len(data); i++ {
		if data[i] != '\n' && data[i] != '\r' {
			continue
		}
		end := int64(i)
		next := end + 1
		terminated := true
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			next = end + 2
			i++
		}
		lines = append(lines, textLine{
			Text:       string(data[start:end]),
			Start:      start,
			End:        end,
			Next:       next,
			Terminated: terminated,
		})
		start = next
	}
	if start < int64(len(data)) {
		lines = append(lines, textLine{
			Text:  string(data[start:]),
			Start: start,
			End:   int64(len(data)),
			Next:  int64(len(data)),
		})
	}
	return lines
}

type bomIssue struct {
	enc  string
	span core.Span
}

// stripBOM removes a UTF-8 byte order mark and reports its span. UTF-16 BOMs
// are reported as unsupported because the parsers operate on UTF-8 text.
func stripBOM(data []byte) (body []byte, shift int64, bom string, issue *bomIssue) {
	switch {
	case len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF:
		return data[3:], 3, "utf-8", nil
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE:
		return data, 0, "", &bomIssue{enc: "utf-16le", span: core.Span{Start: 0, End: 2}}
	case len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF:
		return data, 0, "", &bomIssue{enc: "utf-16be", span: core.Span{Start: 0, End: 2}}
	}
	return data, 0, "", nil
}

// blankLine reports whether a line carries no visible content.
func blankLine(s string) bool { return strings.TrimSpace(s) == "" }

// firstInvalidUTF8 returns the byte offset of the first invalid UTF-8
// sequence, or -1. Subtitle text must be valid UTF-8 for stable output.
func firstInvalidUTF8(data []byte) int64 {
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			return int64(i)
		}
		i += size
	}
	return -1
}

// trimSpaceSpan narrows [start,end) past leading and trailing ASCII spaces
// and tabs. It never crosses the half-open bound.
func trimSpaceSpan(data []byte, start, end int64) core.Span {
	for start < end && (data[start] == ' ' || data[start] == '\t') {
		start++
	}
	for end > start && (data[end-1] == ' ' || data[end-1] == '\t') {
		end--
	}
	return core.Span{Start: start, End: end}
}
