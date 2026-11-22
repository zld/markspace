package timeline

import (
	"fmt"
	"strconv"
	"strings"
)

// Cue is one subtitle interval on the shared nanosecond base.
type Cue struct {
	Index    int
	StartNS  int64
	EndNS    int64
	Text     string
	RawStart string
	RawEnd   string
}

// parseCueTimestamp parses SRT (HH:MM:SS,mmm) and WebVTT (HH:MM:SS.mmm,
// optionally MM:SS.mmm) cue timestamps into integer nanoseconds.
func parseCueTimestamp(s string) (int64, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, fmt.Errorf("%w: empty cue timestamp", ErrMalformed)
	}
	sep := byte(':')
	dot := byte('.')
	if strings.Contains(t, ",") {
		dot = ','
	}
	p := strings.Split(t, string([]byte{sep}))
	if len(p) < 2 || len(p) > 3 {
		return 0, fmt.Errorf("%w: %q", ErrMalformed, s)
	}
	last := strings.SplitN(p[len(p)-1], string([]byte{dot}), 2)
	if len(last) != 2 || len(last[1]) != 3 {
		return 0, fmt.Errorf("%w: milliseconds must have 3 digits: %q", ErrMalformed, s)
	}
	parts := make([]int64, 0, 4)
	for i, seg := range p {
		if i == len(p)-1 {
			seg = last[0]
		}
		n, err := strconv.ParseInt(seg, 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%w: %q", ErrMalformed, s)
		}
		parts = append(parts, n)
	}
	ms, err := strconv.ParseInt(last[1], 10, 64)
	if err != nil || ms < 0 || ms > 999 {
		return 0, fmt.Errorf("%w: %q", ErrMalformed, s)
	}
	var h, m, sec int64
	switch len(parts) {
	case 3:
		h, m, sec = parts[0], parts[1], parts[2]
	case 2:
		h, m, sec = 0, parts[0], parts[1]
	}
	if m > 59 || sec > 59 {
		return 0, fmt.Errorf("%w: %q", ErrMalformed, s)
	}
	return ((h*3600+m*60+sec)*1000+ms)*1_000_000, nil
}

// ParseCue builds one cue, preserving raw labels and rejecting empty or
// backwards (end <= start) intervals.
func ParseCue(index int, rawStart, rawEnd, text string) (Cue, error) {
	st, err := parseCueTimestamp(rawStart)
	if err != nil {
		return Cue{}, err
	}
	en, err := parseCueTimestamp(rawEnd)
	if err != nil {
		return Cue{}, err
	}
	if en <= st {
		return Cue{}, fmt.Errorf("%w: cue %d end %s not after start %s", ErrBackward, index, rawEnd, rawStart)
	}
	return Cue{Index: index, StartNS: st, EndNS: en, Text: strings.TrimSpace(text),
		RawStart: strings.TrimSpace(rawStart), RawEnd: strings.TrimSpace(rawEnd)}, nil
}

// ParseSRT parses a complete SRT document. A blank line terminates each cue.
func ParseSRT(doc string) ([]Cue, error) {
	blocks := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n\n")
	cues := make([]Cue, 0, len(blocks))
	for _, b := range blocks {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		lines := strings.Split(b, "\n")
		if len(lines) < 2 {
			return nil, fmt.Errorf("%w: cue block missing timing line", ErrMalformed)
		}
		idx := len(cues) + 1
		timing := lines[0]
		if n, err := strconv.Atoi(strings.TrimSpace(lines[0])); err == nil {
			idx = n
			lines = lines[1:]
			if len(lines) < 1 {
				return nil, fmt.Errorf("%w: cue %d missing timing line", ErrMalformed, idx)
			}
			timing = lines[0]
		}
		halves := strings.Split(timing, "-->")
		if len(halves) != 2 {
			return nil, fmt.Errorf("%w: cue %d timing %q", ErrMalformed, idx, timing)
		}
		cue, err := ParseCue(idx, halves[0], halves[1], strings.Join(lines[1:], " "))
		if err != nil {
			return nil, err
		}
		cues = append(cues, cue)
	}
	return cues, nil
}

// ParseWebVTT parses a WebVTT document, ignoring the header and NOTE blocks.
func ParseWebVTT(doc string) ([]Cue, error) {
	doc = strings.ReplaceAll(doc, "\r\n", "\n")
	blocks := strings.Split(doc, "\n\n")
	cues := make([]Cue, 0)
	for _, b := range blocks {
		b = strings.TrimSpace(b)
		if b == "" || strings.HasPrefix(b, "WEBVTT") || strings.HasPrefix(b, "NOTE") {
			continue
		}
		lines := strings.Split(b, "\n")
		timingIdx := 0
		if !strings.Contains(lines[0], "-->") {
			timingIdx = 1 // optional cue identifier line
		}
		if timingIdx >= len(lines) {
			return nil, fmt.Errorf("%w: cue missing timing line", ErrMalformed)
		}
		halves := strings.Split(lines[timingIdx], "-->")
		if len(halves) != 2 {
			return nil, fmt.Errorf("%w: cue timing %q", ErrMalformed, lines[timingIdx])
		}
		cue, err := ParseCue(len(cues)+1, halves[0], halves[1], strings.Join(lines[timingIdx+1:], " "))
		if err != nil {
			return nil, err
		}
		cues = append(cues, cue)
	}
	return cues, nil
}

// NanosToTimecode renders a nanosecond position as a frame label for r.
func (r Rate) NanosToTimecode(ns int64) (Timecode, error) {
	if ns < 0 {
		return Timecode{}, fmt.Errorf("%w: negative position %d", ErrBackward, ns)
	}
	fps := r.nominalFPS()
	frame := uint64(0)
	if r.Num == 30000 && r.Den == 1001 {
		// Invert rounded mapping: nearest nominal frame.
		frame = uint64((uint64(ns)*30000 + 500_499_999) / 1_001_000_000_000)
	} else {
		frame = uint64(ns) * uint64(r.Num) / (uint64(r.Den) * uint64(NanosPerSecond))
	}
	var h, m, s, f uint64
	totalMinutes := frame / 1800
	if r.Drop {
		// Invert F = L - 2*(T - T/10) by iterating on the minute count T.
		T := frame / 1798
		for {
			dropped := 2 * (T - T/10)
			label := frame + dropped
			T2 := label / 1800
			if T2 == T {
				totalMinutes = T
				rem := label % 1800
				h = T / 60
				m = T % 60
				s = rem / 30
				f = rem % 30
				break
			}
			T = T2
		}
	} else {
		secsInMin := frame % 1800
		h = totalMinutes / 60
		m = totalMinutes % 60
		s = secsInMin / uint64(fps)
		f = secsInMin % uint64(fps)
	}
	tc := Timecode{H: int(h), M: int(m), S: int(s), F: int(f)}
	if err := r.Validate(tc); err != nil {
		return Timecode{}, err
	}
	return tc, nil
}
