package subtitle

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"packcheck/internal/clock"
	"packcheck/internal/model"
)

// ParseSRT streams an SRT document. Cues separated by blank lines; every
// reported issue carries the byte offset of the offending line.
func ParseSRT(r io.Reader, rate clock.Rate, cuts CutPoints) (*ParseResult, error) {
	res := &ParseResult{Format: "srt", Rate: rate}
	rd := newOffsetReader(r)
	var prev *Cue
	for {
		// Skip blank separators, remembering positions for byte accounting.
		line, off, ok := rd.ReadLine()
		if !ok {
			break
		}
		res.Bytes = rd.pos
		if strings.TrimSpace(line) == "" {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			res.Issues.Add(model.Issue{
				Code: model.CodeUnsupportedInput, Severity: model.SeverityError,
				Layer: model.LayerSubtitle, ByteOffset: off,
				Message: fmt.Sprintf("expected numeric SRT index, got %q", line),
			})
			return res, fmt.Errorf("srt parse error at offset %d: bad cue index", off)
		}
		timing, tOff, ok := rd.ReadLine()
		if !ok {
			return res, fmt.Errorf("srt parse error at offset %d: dangling cue index", off)
		}
		res.Bytes = rd.pos
		start, end, perr := parseSRTRange(timing, rate)
		if perr != nil {
			res.Issues.Add(model.Issue{
				Code: model.CodeUnsupportedInput, Severity: model.SeverityError,
				Layer: model.LayerSubtitle, ByteOffset: tOff, Message: perr.Error(),
			})
			return res, fmt.Errorf("srt parse error at offset %d: %w", tOff, perr)
		}
		var text []string
		for {
			body, bOff, more := rd.ReadLine()
			if !more {
				res.Bytes = rd.pos
				break
			}
			res.Bytes = rd.pos
			if strings.TrimSpace(body) == "" {
				break
			}
			_ = bOff
			text = append(text, body)
		}
		cue := Cue{
			Index: idx, Start: start, End: end, Text: strings.Join(text, "\n"),
			StartOffset: off, EndOffset: rd.pos,
		}
		validateCue(prev, &cue, cuts, &res.Issues, "srt")
		if cue.End.Cmp(cue.Start) >= 0 {
			res.Cues = append(res.Cues, cue)
			prev = &res.Cues[len(res.Cues)-1]
		}
	}
	return res, nil
}

// parseSRTRange parses "HH:MM:SS,mmm --> HH:MM:SS,mmm".
func parseSRTRange(line string, rate clock.Rate) (clock.Rat, clock.Rat, error) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return clock.Rat{}, clock.Rat{}, fmt.Errorf("missing --> arrow in %q", line)
	}
	start, err := parseSRTClock(strings.TrimSpace(parts[0]))
	if err != nil {
		return clock.Rat{}, clock.Rat{}, err
	}
	end, err := parseSRTClock(strings.TrimSpace(parts[1]))
	if err != nil {
		return clock.Rat{}, clock.Rat{}, err
	}
	return start, end, nil
}

func parseSRTClock(token string) (clock.Rat, error) {
	fields := strings.Split(token, ":")
	if len(fields) != 3 {
		return clock.Rat{}, fmt.Errorf("invalid SRT clock %q", token)
	}
	h, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return clock.Rat{}, fmt.Errorf("invalid SRT clock %q", token)
	}
	m, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return clock.Rat{}, fmt.Errorf("invalid SRT clock %q", token)
	}
	secParts := strings.SplitN(fields[2], ",", 2)
	if len(secParts) != 2 {
		return clock.Rat{}, fmt.Errorf("SRT clock requires millisecond field %q", token)
	}
	s, err := strconv.ParseInt(secParts[0], 10, 64)
	if err != nil {
		return clock.Rat{}, fmt.Errorf("invalid SRT clock %q", token)
	}
	ms, err := strconv.ParseInt(secParts[1], 10, 64)
	if err != nil || len(secParts[1]) != 3 {
		return clock.Rat{}, fmt.Errorf("invalid SRT milliseconds %q", token)
	}
	return clock.NewRat(h*3600_000_000+m*60_000_000+s*1_000_000+ms*1000, 1_000_000)
}
