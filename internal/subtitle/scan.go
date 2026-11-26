// Package subtitle parses UTF-8 SRT and WebVTT subtitle files into canonical
// track.Cue values, attributing every failure to a stage and input line.
package subtitle

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"unicode/utf8"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
	"mediamerge/internal/track"
)

// rawBlock is one blank-line separated input block with its starting line.
type rawBlock struct {
	startLine int
	lines     []string
}

// scanBlocks splits r into blocks, rejecting invalid UTF-8 immediately and
// reporting the 1-based line number of the offending bytes.
func scanBlocks(source string, r io.Reader) ([]rawBlock, error) {
	br := bufio.NewReader(r)
	var blocks []rawBlock
	var cur rawBlock
	lineNo := 0
	flush := func() {
		if len(cur.lines) > 0 {
			blocks = append(blocks, cur)
			cur = rawBlock{}
		}
	}
	for {
		raw, err := br.ReadBytes('\n')
		if len(raw) > 0 {
			lineNo++
			if !utf8.Valid(raw) {
				return nil, fault.AtLine(fault.StageParse, fault.KindEncoding, source, lineNo,
					"input is not valid UTF-8")
			}
			line := strings.TrimRight(string(raw), "\r\n")
			if strings.TrimSpace(line) == "" {
				flush()
			} else {
				if len(cur.lines) == 0 {
					cur.startLine = lineNo
				}
				cur.lines = append(cur.lines, line)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fault.New(fault.StageIO, fault.KindIO, source, err.Error())
		}
	}
	flush()
	return blocks, nil
}

// stripBOM removes one leading UTF-8 BOM if present.
func stripBOM(b []byte) []byte { return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}) }

// parseClock parses an SRT-style HH:MM:SS,mmm or VTT-style HH:MM:SS.mmm
// timestamp into ticks. Hours are optional per the WebVTT grammar.
func parseClock(s string) (clock.Ticks, error) {
	s = strings.TrimSpace(s)
	sep := byte(',')
	frac := ""
	if i := strings.IndexByte(s, ','); i >= 0 {
		frac = s[i+1:]
		s = s[:i]
	} else if i := strings.IndexByte(s, '.'); i >= 0 {
		sep = '.'
		frac = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fault.New(fault.StageParse, fault.KindTimecode, "",
			"timestamp must be MM:SS"+string(sep)+"mmm or HH:MM:SS"+string(sep)+"mmm")
	}
	var hh, mm, ss int64
	base := 0
	if len(parts) == 3 {
		hh = mustInt(parts[0])
		base = 1
	}
	mm = mustInt(parts[base])
	ss = mustInt(parts[base+1])
	if mm < 0 || ss < 0 || mm >= 60 || ss >= 60 {
		return 0, fault.New(fault.StageParse, fault.KindTimecode, "", "minute/second out of range")
	}
	fracTicks := int64(0)
	if frac != "" {
		if len(frac) > 9 {
			return 0, fault.New(fault.StageParse, fault.KindTimecode, "", "fractional seconds exceed nanoseconds")
		}
		padded := frac + strings.Repeat("0", 3-len(frac))
		if len(frac) > 3 {
			padded = frac[:3]
		}
		fracTicks = int64(mustInt(padded)) * (clock.Resolution / 1000)
	}
	t, err := clock.FromSeconds((hh*60+mm)*60 + ss)
	if err != nil {
		return 0, err
	}
	return t.Add(clock.Ticks(fracTicks))
}

func mustInt(s string) int64 {
	n := int64(0)
	neg := false
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int64(c-'0')
	}
	if neg {
		return -n
	}
	return n
}

// buildCue finalizes one parsed cue, enforcing non-reversed intervals.
func buildCue(source, version string, seq int, start, end clock.Ticks, text []string) (track.Cue, error) {
	if end < start {
		return track.Cue{}, fault.AtLine(fault.StageParse, fault.KindReversed, source, 0,
			"cue end precedes start")
	}
	c := track.Cue{
		ID:      cueID(source, seq),
		Index:   seq,
		Start:   start,
		End:     end,
		Text:    strings.TrimRight(strings.Join(text, "\n"), "\n"),
		Source:  source,
		Version: version,
	}
	return c, nil
}

func cueID(source string, seq int) string {
	var b strings.Builder
	b.WriteString(source)
	b.WriteByte('#')
	b.WriteString(itoa(seq))
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// parseInterval splits "start --> end" into two tick timestamps.
func parseInterval(line string) (clock.Ticks, clock.Ticks, error) {
	return parseIntervalOpt(line, Options{Rate: clock.Rate25})
}

// parseIntervalOpt additionally rejects drop-frame punctuation that is
// illegal for the configured frame rate.
func parseIntervalOpt(line string, opt Options) (clock.Ticks, clock.Ticks, error) {
	arrow := "-->"
	i := strings.Index(line, arrow)
	if i < 0 {
		return 0, 0, fault.New(fault.StageParse, fault.KindTimecode, "",
			"timestamp line must contain -->: "+strings.TrimSpace(line))
	}
	left := strings.TrimSpace(line[:i])
	right := strings.TrimSpace(line[i+len(arrow):])
	if sp := strings.IndexAny(right, " \t"); sp >= 0 {
		right = right[:sp]
	}
	if err := checkDropFrame(left, opt); err != nil {
		return 0, 0, err
	}
	if err := checkDropFrame(right, opt); err != nil {
		return 0, 0, err
	}
	start, err := parseClock(left)
	if err != nil {
		return 0, 0, err
	}
	end, err := parseClock(right)
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

// checkDropFrame flags ';' separators: a DF label on a non-DF-capable rate is
// an explicit fault rather than a silently corrected timestamp.
func checkDropFrame(ts string, opt Options) error {
	if !strings.ContainsRune(ts, ';') {
		return nil
	}
	if _, _, err := clock.ParseTimecode(strings.ReplaceAll(ts, ".", ":"), opt.Rate); err != nil {
		fe := fault.As(err)
		kind := fault.KindDropFrame
		if fe != nil && fe.Kind == fault.KindTimecode {
			kind = fault.KindTimecode
		}
		return fault.New(fault.StageParse, kind, "",
			"illegal drop-frame label "+ts+": "+err.Error())
	}
	return nil
}
