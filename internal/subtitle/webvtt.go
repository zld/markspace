package subtitle

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"packcheck/internal/clock"
	"packcheck/internal/model"
)

// ParseWebVTT streams a WebVTT document. It accepts the WEBVTT header,
// optional NOTE/STYLE/REGION blocks and cues with an optional identifier line.
func ParseWebVTT(r io.Reader, rate clock.Rate, cuts CutPoints) (*ParseResult, error) {
	res := &ParseResult{Format: "vtt", Rate: rate}
	rd := newOffsetReader(r)
	first, _, ok := rd.ReadLine()
	if !ok || !strings.HasPrefix(strings.TrimSpace(first), "WEBVTT") {
		return res, fmt.Errorf("webvtt parse error at offset 0: missing WEBVTT header")
	}
	res.Bytes = rd.pos
	var prev *Cue
	cueSeq := 0
	for {
		line, lOff, more := rd.ReadLine()
		if !more {
			break
		}
		res.Bytes = rd.pos
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip non-cue blocks up to the next blank line.
		if strings.HasPrefix(trimmed, "NOTE") || strings.HasPrefix(trimmed, "STYLE") ||
			strings.HasPrefix(trimmed, "REGION") {
			for {
				b, _, ok := rd.ReadLine()
				if !ok {
					res.Bytes = rd.pos
					break
				}
				res.Bytes = rd.pos
				if strings.TrimSpace(b) == "" {
					break
				}
			}
			continue
		}
		cueStart := lOff
		timingLine := trimmed
		if !strings.Contains(timingLine, "-->") {
			// Identifier line: the next line must carry the timing.
			next, nOff, ok := rd.ReadLine()
			if !ok || !strings.Contains(next, "-->") {
				res.Issues.Add(model.Issue{
					Code: model.CodeUnsupportedInput, Severity: model.SeverityError,
					Layer: model.LayerSubtitle, ByteOffset: lOff,
					Message: "webvtt cue identifier without timing line",
				})
				return res, fmt.Errorf("webvtt parse error at offset %d", lOff)
			}
			res.Bytes = rd.pos
			timingLine = strings.TrimSpace(next)
			_ = nOff
		}
		start, end, perr := parseVTTRange(timingLine)
		if perr != nil {
			res.Issues.Add(model.Issue{
				Code: model.CodeUnsupportedInput, Severity: model.SeverityError,
				Layer: model.LayerSubtitle, ByteOffset: cueStart, Message: perr.Error(),
			})
			return res, fmt.Errorf("webvtt parse error at offset %d: %w", cueStart, perr)
		}
		var text []string
		for {
			body, _, ok := rd.ReadLine()
			if !ok {
				res.Bytes = rd.pos
				break
			}
			res.Bytes = rd.pos
			if strings.TrimSpace(body) == "" {
				break
			}
			text = append(text, body)
		}
		cueSeq++
		cue := Cue{
			Index: cueSeq, Start: start, End: end, Text: strings.Join(text, "\n"),
			StartOffset: cueStart, EndOffset: rd.pos,
		}
		validateCue(prev, &cue, cuts, &res.Issues, "vtt")
		if cue.End.Cmp(cue.Start) >= 0 {
			res.Cues = append(res.Cues, cue)
			prev = &res.Cues[len(res.Cues)-1]
		}
	}
	return res, nil
}

// parseVTTRange parses "HH:MM:SS.mmm --> HH:MM:SS.mmm [settings]".
func parseVTTRange(line string) (clock.Rat, clock.Rat, error) {
	arrow := strings.Index(line, "-->")
	if arrow < 0 {
		return clock.Rat{}, clock.Rat{}, fmt.Errorf("missing --> arrow in %q", line)
	}
	left := strings.TrimSpace(line[:arrow])
	rest := strings.TrimSpace(line[arrow+3:])
	// Timing ends at the first space; remainder is cue settings.
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		rest = rest[:sp]
	}
	start, err := parseVTTClock(left)
	if err != nil {
		return clock.Rat{}, clock.Rat{}, err
	}
	end, err := parseVTTClock(rest)
	if err != nil {
		return clock.Rat{}, clock.Rat{}, err
	}
	return start, end, nil
}

func parseVTTClock(token string) (clock.Rat, error) {
	fields := strings.Split(token, ":")
	if len(fields) < 2 || len(fields) > 3 {
		return clock.Rat{}, fmt.Errorf("invalid webvtt clock %q", token)
	}
	var h, m int64
	var secField string
	if len(fields) == 3 {
		v, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return clock.Rat{}, fmt.Errorf("invalid webvtt clock %q", token)
		}
		h = v
		m, err = strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return clock.Rat{}, fmt.Errorf("invalid webvtt clock %q", token)
		}
		secField = fields[2]
	} else {
		var err error
		m, err = strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return clock.Rat{}, fmt.Errorf("invalid webvtt clock %q", token)
		}
		secField = fields[1]
	}
	secParts := strings.SplitN(secField, ".", 2)
	if len(secParts) != 2 {
		return clock.Rat{}, fmt.Errorf("webvtt clock requires fractional seconds %q", token)
	}
	s, err := strconv.ParseInt(secParts[0], 10, 64)
	if err != nil {
		return clock.Rat{}, fmt.Errorf("invalid webvtt clock %q", token)
	}
	frac := secParts[1]
	v, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return clock.Rat{}, fmt.Errorf("invalid webvtt clock %q", token)
	}
	// Normalize the fractional part to microseconds.
	micros := v
	for i := len(frac); i < 6; i++ {
		micros *= 10
	}
	return clock.NewRat(h*3600_000_000+m*60_000_000+s*1_000_000+micros, 1_000_000)
}
