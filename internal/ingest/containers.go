package ingest

import (
	"fmt"
	"strconv"
	"strings"

	"example.com/mediatimeline/internal/clock"
)

// Cue is a subtitle cue with times expressed in milliseconds.
type Cue struct {
	ID       string
	StartMS  int64
	EndMS    int64
	Settings string
	Text     string
}

// ParseWebVTT parses the cue blocks of a WebVTT document.
func ParseWebVTT(doc []byte) ([]Cue, error) {
	lines := strings.Split(strings.ReplaceAll(string(doc), "\r\n", "\n"), "\n")
	var cues []Cue
	var errs []string
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "WEBVTT") || strings.HasPrefix(line, "NOTE") {
			continue
		}
		// Optional cue identifier on the line before the timing arrow.
		id := ""
		if !strings.Contains(line, "-->") {
			id = line
			i++
			if i >= len(lines) {
				break
			}
			line = strings.TrimSpace(lines[i])
		}
		start, end, settings, err := parseCueTiming(line)
		if err != nil {
			errs = append(errs, fmt.Sprintf("line %d: %v", i+1, err))
			continue
		}
		var text []string
		for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" {
			i++
			text = append(text, lines[i])
		}
		if end <= start {
			end = start + 1000
			errs = append(errs, fmt.Sprintf("cue %q has non-positive duration", id))
		}
		cues = append(cues, Cue{ID: id, StartMS: start, EndMS: end, Settings: settings, Text: strings.Join(text, "\n")})
	}
	if len(errs) > 0 {
		return cues, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return cues, nil
}

func parseCueTiming(line string) (start, end int64, settings string, err error) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return 0, 0, "", fmt.Errorf("missing --> in %q", line)
	}
	if start, err = parseVTTTime(strings.TrimSpace(parts[0])); err != nil {
		return 0, 0, "", err
	}
	rest := strings.Fields(strings.TrimSpace(parts[1]))
	if len(rest) == 0 {
		return 0, 0, "", fmt.Errorf("missing cue end time")
	}
	if end, err = parseVTTTime(rest[0]); err != nil {
		return 0, 0, "", err
	}
	return start, end, strings.Join(rest[1:], " "), nil
}

func parseVTTTime(s string) (int64, error) {
	fields := strings.Split(s, ":")
	if len(fields) < 2 || len(fields) > 3 {
		return 0, fmt.Errorf("bad cue timestamp %q", s)
	}
	secField := fields[len(fields)-1]
	secParts := strings.SplitN(secField, ".", 2)
	sec, err := strconv.ParseInt(secParts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad seconds in %q", s)
	}
	ms := int64(0)
	if len(secParts) == 2 {
		frac := secParts[1]
		if len(frac) > 3 {
			frac = frac[:3]
		}
		for len(frac) < 3 {
			frac += "0"
		}
		if ms, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return 0, fmt.Errorf("bad milliseconds in %q", s)
		}
	}
	min, err := strconv.ParseInt(fields[len(fields)-2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad minutes in %q", s)
	}
	hours := int64(0)
	if len(fields) == 3 {
		if hours, err = strconv.ParseInt(fields[0], 10, 64); err != nil {
			return 0, fmt.Errorf("bad hours in %q", s)
		}
	}
	return ((hours*60+min)*60+sec)*1000 + ms, nil
}

// Chapter is a chapter mark with a time in milliseconds.
type Chapter struct {
	Title   string
	StartMS int64
}

// ParseChapters parses a simple chapter list of the form
// "CHAPTER01=00:00:00.000" followed by "CHAPTER01NAME=Opening".
func ParseChapters(doc []byte) ([]Chapter, error) {
	var out []Chapter
	index := map[string]int{}
	for _, raw := range strings.Split(string(doc), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			return out, fmt.Errorf("malformed chapter line %q", line)
		}
		key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if strings.HasSuffix(key, "NAME") {
			if i, ok := index[strings.TrimSuffix(key, "NAME")]; ok {
				out[i].Title = val
			}
			continue
		}
		ms, err := parseVTTTime(val)
		if err != nil {
			return out, err
		}
		index[key] = len(out)
		out = append(out, Chapter{StartMS: ms, Title: key})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no chapters found")
	}
	return out, nil
}

// msToTicks converts milliseconds to ticks at the given timescale exactly.
func msToTicks(ms int64, timescale uint32) (int64, error) {
	r, err := clock.New(ms, 1000)
	if err != nil {
		return 0, err
	}
	return r.FloorTicks(timescale)
}
