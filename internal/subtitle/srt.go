package subtitle

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"mediaarchive/internal/clock"
	"mediaarchive/internal/diag"
)

// Cue is one subtitle entry mapped onto the unified rational time base.
type Cue struct {
	Index    int
	Start    clock.Rational
	End      clock.Rational
	Text     string
	Offset   int64 // byte offset of the cue's first byte in the source
	Source   string
	Settings string // WebVTT cue settings, empty for SRT
	ID       string // WebVTT cue identifier, empty for SRT
}

// Duration returns End-Start, always non-negative for accepted cues.
func (c Cue) Duration() clock.Rational { return c.End.Sub(c.Start) }

// ParseResult collects accepted cues plus every structured fault found along
// the way. Parsing is resumable by design: a bad record is recorded and the
// scanner continues at the next blank-line boundary.
type ParseResult struct {
	Source string
	Cues   []Cue
	Faults []*diag.Fault
	Bytes  int64
}

// Err returns the first fault, if any, wrapped so callers can use diag.As.
func (r *ParseResult) Err() error {
	if len(r.Faults) == 0 {
		return nil
	}
	return r.Faults[0]
}

// HasCode reports whether any fault carries code.
func (r *ParseResult) HasCode(code string) bool {
	for _, f := range r.Faults {
		if f.Code == code {
			return true
		}
	}
	return false
}

// MaxCueBytes bounds a single cue body so a corrupt file cannot force
// unbounded memory growth; oversized cues are faults, not fatal errors.
const MaxCueBytes = 1 << 20

// ReadFile parses a subtitle file by sniffing WebVTT vs SRT. Input is opened
// read-only and never modified.
func ReadFile(path string) (*ParseResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(bytes.NewReader(data), path), nil
}

// Parse dispatches on the WEBVTT signature after BOM removal.
func Parse(r io.Reader, source string) *ParseResult {
	data, err := io.ReadAll(io.LimitReader(r, 64<<20))
	res := &ParseResult{Source: source}
	if err != nil {
		res.Faults = append(res.Faults, diag.New(diag.CodeTruncated, 0, "read %s: %v", source, err))
		return res
	}
	out := parseBytes(data, source)
	out.Bytes = int64(len(data))
	return out
}

func parseBytes(data []byte, source string) *ParseResult {
	body, bom := stripBOM(data)
	offsetBase := int64(len(data) - len(body))
	var res *ParseResult
	if bytes.HasPrefix(body, []byte("WEBVTT")) {
		res = parseVTT(body, source)
	} else {
		res = parseSRT(body, source)
	}
	if bom {
		// Offsets must refer to the operator's real file, including the BOM.
		for _, f := range res.Faults {
			f.Offset += offsetBase
		}
		for i := range res.Cues {
			res.Cues[i].Offset += offsetBase
		}
	}
	return res
}

func stripBOM(data []byte) ([]byte, bool) {
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		return data[3:], true
	}
	return data, false
}

// splitBlocks returns blank-line separated blocks with absolute offsets.
// Empty runs of blank lines collapse into a single boundary.
func splitBlocks(data []byte) []block {
	var out []block
	start := 0
	i := 0
	for i < len(data) {
		j := i
		for j < len(data) && data[j] != '\n' {
			j++
		}
		if len(bytes.TrimSpace(data[i:j])) == 0 {
			if i > start {
				out = append(out, block{off: int64(start), raw: data[start:i]})
			}
			k := j
			for k < len(data) && data[k] == '\n' {
				k++
			}
			start = k
			i = k
			continue
		}
		if j >= len(data) {
			break
		}
		i = j + 1
	}
	if start < len(data) {
		out = append(out, block{off: int64(start), raw: data[start:]})
	}
	return out
}

type block struct {
	off int64
	raw []byte
}

func lines(b block) []string {
	s := strings.ReplaceAll(string(b.raw), "\r\n", "\n")
	s = strings.Trim(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// parseTimestamp accepts HH:MM:SS,mmm, HH:MM:SS.mmm, HH:MM:SS:mmm and the
// microsecond variants with six fractional digits. Returned position is
// exact, and the digit count decides ms vs µs unambiguously.
func parseTimestamp(s string) (clock.Rational, error) { return parseTimestampFlex(s, false) }

// parseTimestampFlex optionally accepts the WebVTT MM:SS.mmm short form.
func parseTimestampFlex(s string, allowShort bool) (clock.Rational, error) {
	s = strings.TrimSpace(s)
	sep := strings.IndexAny(s, ",.")
	if sep < 0 {
		return clock.Zero, fmt.Errorf("timestamp %q has no fractional separator", s)
	}
	frac := s[sep+1:]
	main := s[:sep]
	if len(frac) != 3 && len(frac) != 6 {
		return clock.Zero, fmt.Errorf("timestamp %q fractional part must be 3 (ms) or 6 (µs) digits", s)
	}
	for _, r := range frac {
		if r < '0' || r > '9' {
			return clock.Zero, fmt.Errorf("timestamp %q has non-digit fraction", s)
		}
	}
	parts := strings.Split(main, ":")
	if len(parts) == 2 && allowShort {
		parts = append([]string{"0"}, parts...)
	}
	if len(parts) != 3 {
		return clock.Zero, fmt.Errorf("timestamp %q must be HH:MM:SS", s)
	}
	var h, m, sec int64
	var err error
	if h, err = strconv.ParseInt(parts[0], 10, 32); err != nil || h > 9999 {
		return clock.Zero, fmt.Errorf("timestamp %q bad hours", s)
	}
	if m, err = strconv.ParseInt(parts[1], 10, 32); err != nil || m > 59 {
		return clock.Zero, fmt.Errorf("timestamp %q bad minutes", s)
	}
	if sec, err = strconv.ParseInt(parts[2], 10, 32); err != nil || sec > 59 {
		return clock.Zero, fmt.Errorf("timestamp %q bad seconds", s)
	}
	if h < 0 || m < 0 || sec < 0 {
		return clock.Zero, fmt.Errorf("timestamp %q negative", s)
	}
	var f int64
	if len(frac) == 3 {
		f, _ = strconv.ParseInt(frac, 10, 32)
		return clock.NewRat(h*3600000+m*60000+sec*1000+f, 1000), nil
	}
	f, _ = strconv.ParseInt(frac, 10, 32)
	return clock.NewRat(h*3600000000+m*60000000+sec*1000000+f, 1000000), nil
}

func parseSRT(data []byte, source string) *ParseResult {
	res := &ParseResult{Source: source}
	seenIndex := 0
	// A record with a valid timing line but no payload text, or a final record
	// with no --> at all, is damage from a stopped capture rather than bad
	// authoring. Both are reported as CodeTruncated at the record's offset.
	for _, b := range splitBlocks(data) {
		ls := lines(b)
		if len(ls) == 0 {
			continue
		}
		if isSkippable(ls[0]) {
			continue
		}
		if len(b.raw) > MaxCueBytes {
			res.Faults = append(res.Faults, diag.New(diag.CodeTruncated, b.off,
				"srt cue block of %d bytes exceeds %d", len(b.raw), MaxCueBytes))
			continue
		}
		// Optional numeric index line; if the first line is not an index the
		// block is still parsed for compatibility, with index -1.
		idx := -1
		timingLine := 0
		if n, err := strconv.Atoi(strings.TrimSpace(ls[0])); err == nil {
			idx = n
			timingLine = 1
		}
		if timingLine >= len(ls) {
			res.Faults = append(res.Faults, diag.New(diag.CodeTruncated, b.off, "srt cue has no timing line"))
			continue
		}
		start, end, err := parseSRTTiming(ls[timingLine])
		if err != nil {
			// A record cut mid-timestamp has no --> yet; that is a truncated
			// body rather than an illegal time, and needs a different code so
			// operators can tell capture loss from authoring mistakes.
			code := diag.CodeInvalidTime
			if !strings.Contains(ls[timingLine], "-->") {
				code = diag.CodeTruncated
			}
			res.Faults = append(res.Faults, diag.New(code, b.off,
				"srt cue %d timing: %v", firstNonEmpty(idx, seenIndex+1), err))
			continue
		}
		if end.Cmp(start) <= 0 {
			res.Faults = append(res.Faults, diag.New(diag.CodeInvalidTime, b.off,
				"srt cue %d ends before it starts", firstNonEmpty(idx, seenIndex+1)))
			continue
		}
		text := strings.Join(ls[timingLine+1:], "\n")
		if timingLine+1 >= len(ls) {
			res.Faults = append(res.Faults, diag.New(diag.CodeTruncated, b.off,
				"srt cue %d has no payload text", firstNonEmpty(idx, seenIndex+1)))
		}
		seenIndex++
		res.Cues = append(res.Cues, Cue{
			Index:  firstNonEmpty(idx, seenIndex),
			Start:  start,
			End:    end,
			Text:   text,
			Offset: b.off,
			Source: source,
		})
	}
	return res
}

func firstNonEmpty(v, fallback int) int {
	if v >= 0 {
		return v
	}
	return fallback
}

func isSkippable(line string) bool {
	t := strings.TrimSpace(line)
	return t == ""
}

func parseSRTTiming(line string) (clock.Rational, clock.Rational, error) {
	// "00:00:01,000 --> 00:00:03,500" plus optional trailing settings.
	parts := strings.Split(line, "-->")
	if len(parts) != 2 {
		return clock.Zero, clock.Zero, fmt.Errorf("missing --> in %q", strings.TrimSpace(line))
	}
	start, err := parseTimestamp(parts[0])
	if err != nil {
		return clock.Zero, clock.Zero, err
	}
	endField := strings.Fields(strings.TrimSpace(parts[1]))
	if len(endField) == 0 {
		return clock.Zero, clock.Zero, fmt.Errorf("missing end timestamp")
	}
	end, err := parseTimestamp(endField[0])
	if err != nil {
		return clock.Zero, clock.Zero, err
	}
	return start, end, nil
}
