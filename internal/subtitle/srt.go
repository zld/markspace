package subtitle

import (
	"strconv"
	"strings"

	"archivecheck/internal/fault"
	"archivecheck/internal/timebase"
)

// parseSRT scans SRT blocks. A block that fails to parse is recorded and
// skipped by resynchronising on the next blank line; every byte offset is in
// the original file.
func parseSRT(name string, data []byte) ([]Cue, []Problem) {
	src := safeName(name)
	var cues []Cue
	var problems []Problem
	rec := func(off, end int64, kind, format string, args ...any) {
		problems = append(problems, Problem{Offset: off, End: end, Err: fault.New(fault.StageSubtitle, kind, src, off, format, args...)})
	}

	p := &lineScanner{data: data}
	seq := 0
	for {
		// Skip blank lines between blocks.
		for p.peekBlank() {
			p.next()
		}
		if p.done() {
			break
		}
		blockStart := p.off
		var block []textLine
		for !p.done() && !p.peekBlank() {
			block = append(block, p.next())
		}
		if len(block) == 0 {
			continue
		}
		// Optional numeric sequence line.
		i := 0
		var label string
		if _, err := strconv.Atoi(strings.TrimSpace(block[0].Text)); err == nil {
			label = strings.TrimSpace(block[0].Text)
			i = 1
		}
		if i >= len(block) {
			rec(blockStart, p.off, fault.KindTruncated, "subtitle block has a sequence number but no timing line")
			continue
		}
		timing := block[i]
		start, end, body, err := parseSRTTiming(timing.Text)
		if err != nil {
			// Missing --> means the file lost its structure: report this block
			// and keep scanning the next one for a plausible cue start.
			problems = append(problems, Problem{Offset: timing.Off, End: timing.End, Err: located(err, src, timing.Off)})
			continue
		}
		i++
		if body != "" {
			// SRT has no inline settings; text after end time is not valid.
			rec(timing.Off+int64(strings.Index(timing.Text, "-->")), timing.End, fault.KindCorrupt, "unexpected text %q after the SRT end time", clip(body))
			continue
		}
		lines := make([]string, 0, len(block)-i)
		for _, l := range block[i:] {
			lines = append(lines, l.Text)
		}
		if end <= start {
			rec(timing.Off, timing.End, fault.KindInvalid, "cue end %d is not after start %d", int64(end), int64(start))
			continue
		}
		seq++
		cues = append(cues, Cue{Index: seq, ID: label, Start: start, End: end, Lines: lines, Offset: timing.Off})
	}
	return cues, problems
}

// parseSRTTiming parses "HH:MM:SS,mmm --> HH:MM:SS,mmm" and returns any
// leftover text. Timestamps are converted to the nanosecond scale through the
// same clock the timecode subsystem uses.
func parseSRTTiming(s string) (timebase.Nanos, timebase.Nanos, string, error) {
	arrow := strings.Index(s, "-->")
	if arrow < 0 {
		return 0, 0, "", fault.New(fault.StageSubtitle, fault.KindCorrupt, "", 0, "missing -->")
	}
	left := strings.TrimSpace(s[:arrow])
	right := strings.TrimSpace(s[arrow+3:])
	// Split the right side at the first whitespace: anything after is body.
	fields := strings.Fields(right)
	if len(fields) == 0 {
		return 0, 0, "", fault.New(fault.StageSubtitle, fault.KindTruncated, "", int64(arrow), "missing end timestamp")
	}
	endField := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(right, endField))
	start, err := parseSRTTimestamp(left)
	if err != nil {
		return 0, 0, "", err
	}
	end, err := parseSRTTimestamp(endField)
	if err != nil {
		return 0, 0, "", err
	}
	return start, end, rest, nil
}

// parseSRTTimestamp parses HH:MM:SS,mmm (comma or dot) or MM:SS,mmm when the
// hours are omitted.
func parseSRTTimestamp(s string) (timebase.Nanos, error) {
	bad := func(kind, format string, args ...any) (timebase.Nanos, error) {
		return 0, fault.New(fault.StageSubtitle, kind, "", 0, format, args...)
	}
	if strings.HasPrefix(s, "-") {
		return bad(fault.KindInvalid, "negative subtitle timestamp %q", s)
	}
	// The millisecond separator is the last of ':' splitting off ",mmm"/".mmm".
	msSep := strings.LastIndexAny(s, ",.")
	if msSep < 0 {
		return bad(fault.KindInvalid, "subtitle timestamp %q has no milliseconds", s)
	}
	msText := s[msSep+1:]
	if len(msText) != 3 || !digitsOnly(msText) {
		return bad(fault.KindInvalid, "subtitle milliseconds %q are not three digits", msText)
	}
	ms, _ := strconv.ParseInt(msText, 10, 64)
	hms := strings.Split(s[:msSep], ":")
	var hh, mm, ss int64
	var err error
	switch len(hms) {
	case 3:
		if hh, err = strconv.ParseInt(hms[0], 10, 64); err != nil {
			return bad(fault.KindInvalid, "subtitle hours %q are not a number", hms[0])
		}
	case 2:
		// MM:SS,mmm shorthand is accepted by both SRT and WebVTT.
	default:
		return bad(fault.KindInvalid, "subtitle timestamp %q must be HH:MM:SS or MM:SS", s)
	}
	if mm, err = strconv.ParseInt(hms[len(hms)-2], 10, 64); err != nil {
		return bad(fault.KindInvalid, "subtitle minutes %q are not a number", hms[len(hms)-2])
	}
	if ss, err = strconv.ParseInt(hms[len(hms)-1], 10, 64); err != nil {
		return bad(fault.KindInvalid, "subtitle seconds %q are not a number", hms[len(hms)-1])
	}
	if hh < 0 || mm < 0 || ss < 0 {
		return bad(fault.KindInvalid, "negative subtitle timestamp %q", s)
	}
	if mm > 59 || ss > 59 {
		return bad(fault.KindInvalid, "subtitle timestamp %q has out-of-range minutes or seconds", s)
	}
	nanos := ((hh*3600+mm*60+ss)*1000 + ms) * 1e6
	if nanos > int64(timebase.MaxNanos) {
		return bad(fault.KindOverflow, "subtitle timestamp %q exceeds 24 hours", s)
	}
	return timebase.Nanos(nanos), nil
}
