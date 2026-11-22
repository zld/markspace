// Package manifest parses local DASH MPD and HLS M3U8 fixtures into a
// common rational timeline model and aligns representations across rates.
package manifest

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"mediadist/timeline"
)

// Limits keep malformed fixtures from causing unbounded memory growth.
const (
	MaxManifestBytes = 1 << 20
	MaxRepresentations = 16
	MaxSegmentsPerRep  = 10000
	maxAttributeLen    = 4096
)

// Segment is one media fragment on the canonical millisecond timeline.
type Segment struct {
	Epoch    int
	Index    int
	Start    timeline.Rat
	Duration timeline.Rat
	URI      string
}

// End returns start+duration, propagating overflow errors.
func (s Segment) End() (timeline.Rat, error) {
	return s.Start.Add(s.Duration)
}

// Representation is one DASH adaptation-set rendition.
type Representation struct {
	ID       string
	Bandwidth int
	Timescale int64
	Segments  []Segment
}

// MPD is the parsed DASH manifest.
type MPD struct {
	Representations []*Representation
}

// ParseMPD parses the line-oriented local fixture format:
//
//	MPD media-presentation-duration-ms=<n>
//	REP id=<id> bandwidth=<bps> timescale=<ticks-per-second>
//	SEG start=<ticks> duration=<ticks> uri=<name>
//
// All times are converted to reduced rational millisecond values.
func ParseMPD(data []byte) (*MPD, error) {
	if len(data) > MaxManifestBytes {
		return nil, timeline.NewError("MANIFEST_TOO_LARGE", "MPD exceeds size limit",
			map[string]any{"bytes": len(data), "limit": MaxManifestBytes})
	}
	mpd := &MPD{}
	seenIDs := map[string]bool{}
	var cur *Representation
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxAttributeLen*2)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "MPD":
			if len(fields) < 2 {
				return nil, badLine(lineNo, line, "MPD requires attributes")
			}
		case "REP":
			if len(mpd.Representations) >= MaxRepresentations {
				return nil, timeline.NewError("MANIFEST_LIMIT", "too many representations",
					map[string]any{"limit": MaxRepresentations})
			}
			attrs, err := parseAttrs(fields[1:], lineNo)
			if err != nil {
				return nil, err
			}
			id, err := reqAttr(attrs, "id", lineNo)
			if err != nil {
				return nil, err
			}
			if seenIDs[id] {
				return nil, timeline.NewError("DUPLICATE_REPRESENTATION",
					"representation id appears twice",
					map[string]any{"id": id, "line": lineNo})
			}
			ts, err := reqInt(attrs, "timescale", lineNo)
			if err != nil {
				return nil, err
			}
			if ts <= 0 {
				return nil, timeline.NewError("INVALID_TIMESCALE",
					"timescale must be a positive integer",
					map[string]any{"timescale": ts, "line": lineNo})
			}
			bw, err := reqInt(attrs, "bandwidth", lineNo)
			if err != nil {
				return nil, err
			}
			cur = &Representation{ID: id, Bandwidth: int(bw), Timescale: ts}
			mpd.Representations = append(mpd.Representations, cur)
			seenIDs[id] = true
		case "SEG":
			if cur == nil {
				return nil, badLine(lineNo, line, "SEG before REP")
			}
			if len(cur.Segments) >= MaxSegmentsPerRep {
				return nil, timeline.NewError("MANIFEST_LIMIT", "too many segments",
					map[string]any{"rep": cur.ID, "limit": MaxSegmentsPerRep})
			}
			attrs, err := parseAttrs(fields[1:], lineNo)
			if err != nil {
				return nil, err
			}
			startT, err := reqInt(attrs, "start", lineNo)
			if err != nil {
				return nil, err
			}
			durT, err := reqInt(attrs, "duration", lineNo)
			if err != nil {
				return nil, err
			}
			if durT <= 0 {
				return nil, timeline.NewError("ZERO_DURATION_SEGMENT",
					"segment duration must be positive",
					map[string]any{"rep": cur.ID, "line": lineNo})
			}
			start, err := timeline.NewRat(startT, cur.Timescale)
			if err != nil {
				return nil, err
			}
			start, err = start.MulInt(1000)
			if err != nil {
				return nil, err
			}
			dur, err := timeline.NewRat(durT, cur.Timescale)
			if err != nil {
				return nil, err
			}
			dur, err = dur.MulInt(1000)
			if err != nil {
				return nil, err
			}
			cur.Segments = append(cur.Segments, Segment{
				Epoch:    0,
				Index:    len(cur.Segments),
				Start:    start,
				Duration: dur,
				URI:      attrs["uri"],
			})
		default:
			return nil, badLine(lineNo, line, "unknown MPD directive")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, timeline.NewError("MANIFEST_MALFORMED", "scanner failure",
			map[string]any{"error": err.Error()})
	}
	if len(mpd.Representations) == 0 {
		return nil, timeline.NewError("MANIFEST_MALFORMED", "MPD has no representations", nil)
	}
	return mpd, nil
}

func parseAttrs(fields []string, lineNo int) (map[string]string, error) {
	attrs := make(map[string]string, len(fields))
	for _, f := range fields {
		key, val, ok := strings.Cut(f, "=")
		if !ok || key == "" {
			return nil, badLine(lineNo, f, "expected key=value")
		}
		attrs[key] = val
	}
	return attrs, nil
}

func reqAttr(attrs map[string]string, key string, lineNo int) (string, error) {
	v, ok := attrs[key]
	if !ok || v == "" {
		return "", timeline.NewError("MANIFEST_MALFORMED", "missing required attribute",
			map[string]any{"attribute": key, "line": lineNo})
	}
	return v, nil
}

func reqInt(attrs map[string]string, key string, lineNo int) (int64, error) {
	v, err := reqAttr(attrs, key, lineNo)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, timeline.NewError("MANIFEST_MALFORMED", "attribute is not an integer",
			map[string]any{"attribute": key, "value": v, "line": lineNo})
	}
	return n, nil
}

func badLine(lineNo int, line, reason string) error {
	return timeline.NewError("MANIFEST_MALFORMED", reason,
		map[string]any{"line": lineNo, "content": truncate(line)})
}

func truncate(s string) string {
	if len(s) > 128 {
		return s[:128] + "..."
	}
	return s
}
