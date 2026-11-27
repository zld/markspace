package subtitle

import (
	"strconv"
	"strings"

	"archivecheck/internal/fault"
	"archivecheck/internal/timebase"
)

// parseVTT scans WebVTT blocks. It understands the WEBVTT header, NOTE and
// STYLE blocks, cue identifiers, cue settings after the timing line and
// multi-line cue payloads. Header-level syntax problems are recorded as
// non-fatal problems; structural cue problems record the cue's byte span.
func parseVTT(name string, data []byte) ([]Cue, []Problem) {
	src := safeName(name)
	var cues []Cue
	var problems []Problem
	rec := func(off, end int64, kind, format string, args ...any) {
		problems = append(problems, Problem{Offset: off, End: end, Err: fault.New(fault.StageSubtitle, kind, src, off, format, args...)})
	}

	p := &lineScanner{data: data}
	// Signature line.
	if p.done() {
		rec(0, 0, fault.KindTruncated, "WebVTT file is empty")
		return nil, problems
	}
	sig := p.next()
	if !strings.HasPrefix(sig.Text, "WEBVTT") {
		rec(sig.Off, sig.End, fault.KindCorrupt, "missing WEBVTT signature")
	}
	// Header metadata until the first blank line.
	for !p.done() && !p.peekBlank() {
		p.next()
	}

	seq := 0
	for {
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
		head := strings.TrimSpace(block[0].Text)
		if strings.HasPrefix(head, "NOTE") || head == "STYLE" || head == "REGION" {
			continue
		}
		// An identifier line is any line without "-->"; it precedes timing.
		i := 0
		id := ""
		if !strings.Contains(block[0].Text, "-->") {
			id = strings.TrimSpace(block[0].Text)
			i = 1
		}
		if i >= len(block) {
			rec(blockStart, p.off, fault.KindTruncated, "WebVTT cue has an identifier but no timing line")
			continue
		}
		timing := block[i]
		start, end, settings, err := parseVTTTiming(timing.Text)
		if err != nil {
			rec(timing.Off, timing.End, fault.KindCorrupt, "WebVTT cue timing at %q is invalid: %v", clip(timing.Text), stripOffset(err))
			continue
		}
		if end <= start {
			rec(timing.Off, timing.End, fault.KindInvalid, "cue end %d is not after start %d", int64(end), int64(start))
			continue
		}
		if err := validateSettings(settings); err != nil {
			rec(timing.Off+int64(strings.Index(timing.Text, "-->")), timing.End, fault.KindInvalid, "cue settings %q: %v", settings, err)
			continue
		}
		lines := make([]string, 0, len(block)-i-1)
		for _, l := range block[i+1:] {
			lines = append(lines, l.Text)
		}
		seq++
		cues = append(cues, Cue{Index: seq, ID: id, Start: start, End: end, Lines: lines, Settings: settings, Offset: timing.Off})
	}
	return cues, problems
}

// parseVTTTiming parses "start --> end [settings]" where timestamps may use a
// comma or dot decimal separator and may omit hours for durations under an
// hour.
func parseVTTTiming(s string) (timebase.Nanos, timebase.Nanos, string, error) {
	arrow := strings.Index(s, "-->")
	if arrow < 0 {
		return 0, 0, "", fault.New(fault.StageSubtitle, fault.KindCorrupt, "", 0, "missing -->")
	}
	left := strings.TrimSpace(s[:arrow])
	right := strings.TrimSpace(s[arrow+3:])
	fields := strings.Fields(right)
	if len(fields) == 0 {
		return 0, 0, "", fault.New(fault.StageSubtitle, fault.KindTruncated, "", 0, "missing end timestamp")
	}
	start, err := parseVTTSingle(left)
	if err != nil {
		return 0, 0, "", err
	}
	endText := fields[0]
	end, err := parseVTTSingle(endText)
	if err != nil {
		return 0, 0, "", err
	}
	settings := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(right), endText))
	return start, end, settings, nil
}

// parseVTTSingle accepts [HH:]MM:SS.mmm with HH optional.
func parseVTTSingle(s string) (timebase.Nanos, error) {
	bad := func(format string, args ...any) (timebase.Nanos, error) {
		return 0, fault.New(fault.StageSubtitle, fault.KindInvalid, "", 0, format, args...)
	}
	if s == "" {
		return bad("empty WebVTT timestamp")
	}
	if strings.HasPrefix(s, "-") {
		return bad("negative WebVTT timestamp %q", s)
	}
	dot := strings.LastIndexAny(s, ".,")
	if dot < 0 {
		return bad("WebVTT timestamp %q has no fractional seconds", s)
	}
	frac := s[dot+1:]
	if len(frac) != 3 || !digitsOnly(frac) {
		return bad("WebVTT milliseconds %q are not three digits", frac)
	}
	ms, _ := strconv.ParseInt(frac, 10, 64)
	parts := strings.Split(s[:dot], ":")
	if len(parts) != 2 && len(parts) != 3 {
		return bad("WebVTT timestamp %q must be MM:SS.mmm or HH:MM:SS.mmm", s)
	}
	var vals [3]int64
	offset := 3 - len(parts)
	for i, part := range parts {
		v, err := strconv.ParseInt(part, 10, 64)
		if err != nil || v < 0 {
			return bad("WebVTT timestamp %q has a non-numeric field %q", s, part)
		}
		vals[offset+i] = v
	}
	hh, mm, ss := vals[0], vals[1], vals[2]
	if mm > 59 || ss > 59 {
		return bad("WebVTT timestamp %q has out-of-range minutes or seconds", s)
	}
	nanos := ((hh*3600+mm*60+ss)*1000 + ms) * 1e6
	if nanos > int64(timebase.MaxNanos) {
		return bad("WebVTT timestamp %q exceeds 24 hours", s)
	}
	return timebase.Nanos(nanos), nil
}

// validateSettings checks the cue settings grammar of WebVTT: space-separated
// name:value pairs drawn from a fixed set.
func validateSettings(s string) error {
	if s == "" {
		return nil
	}
	valid := map[string]bool{"vertical": true, "line": true, "position": true, "size": true, "align": true, "region": true}
	for _, field := range strings.Fields(s) {
		name, _, ok := strings.Cut(field, ":")
		if !ok {
			return fault.New(fault.StageSubtitle, fault.KindInvalid, "", 0, "setting %q is not name:value", field)
		}
		if !valid[name] {
			return fault.New(fault.StageSubtitle, fault.KindInvalid, "", 0, "unknown cue setting %q", name)
		}
	}
	return nil
}

// stripOffset flattens an error for inclusion in a message without repeating
// its offset prefix.
func stripOffset(err error) string {
	if fe := fault.As(err); fe != nil {
		return fe.Detail
	}
	return err.Error()
}
