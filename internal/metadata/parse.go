// Package metadata parses subtitles and timed metadata carriers.
//
// Every event uses clock.Timestamp. Events from different timescales are
// canonicalized before sorting and de-duplication. Parsers never slice past
// the end of an input; truncated carriers return a fault.Error carrying the
// first unreadable offset.
package metadata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zld/avpipe/internal/clock"
	"github.com/zld/avpipe/internal/fault"
)

// Event is one cross-track timed cue or metadata event.
type Event struct {
	Track        string           `json:"track"`
	Kind         string           `json:"kind"`
	Start        clock.Timestamp  `json:"start"`
	End          *clock.Timestamp `json:"end,omitempty"`
	Text         string           `json:"text,omitempty"`
	Digest       string           `json:"content_digest"`
	SourceOffset int64            `json:"source_offset,omitempty"`
}

// DigestContent returns the deterministic SHA-256 content summary.
func DigestContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// Normalize fills a missing content digest.
func (e Event) Normalize() Event {
	if e.Digest == "" {
		e.Digest = DigestContent([]byte(e.Text))
	}
	return e
}

// SortAndDedupe orders events by canonical media time and removes repeated
// events sharing track, canonical time, and content digest.
func SortAndDedupe(events []Event) []Event {
	normalized := make([]Event, 0, len(events))
	for _, e := range events {
		normalized = append(normalized, e.Normalize())
	}

	sort.SliceStable(normalized, func(i, j int) bool {
		ci := normalized[i].Start.Canonical()
		cj := normalized[j].Start.Canonical()
		if ci.Ticks != cj.Ticks {
			return ci.Ticks < cj.Ticks
		}
		if normalized[i].Track != normalized[j].Track {
			return normalized[i].Track < normalized[j].Track
		}
		if normalized[i].Digest != normalized[j].Digest {
			return normalized[i].Digest < normalized[j].Digest
		}
		return normalized[i].Kind < normalized[j].Kind
	})

	out := make([]Event, 0, len(normalized))
	seen := make(map[string]struct{}, len(normalized))
	for _, e := range normalized {
		c := e.Start.Canonical()
		key := fmt.Sprintf("%s|%d@%d|%s", e.Track, c.Ticks, c.Timescale, e.Digest)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	return out
}

// ParseSRT parses a small deterministic SRT cue sheet. Complete final cues
// without a trailing blank line are accepted.
func ParseSRT(data []byte) ([]Event, error) {
	const component = "metadata/srt"
	var events []Event
	pos := 0

	for pos < len(data) {
		lineStart := pos
		raw, next := readLine(data, pos)
		pos = next
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		timing := line
		timingOffset := lineStart
		if !strings.Contains(timing, "-->") {
			// Optional numeric cue index: the next non-empty line must be timing.
			for pos < len(data) && strings.TrimSpace(mustLine(data, pos)) == "" {
				_, pos = readLine(data, pos)
			}
			if pos >= len(data) {
				return events, fault.Truncated(component, int64(pos), int64(len(data)), "SRT timing line")
			}
			timingOffset = pos
			timing, pos = readLine(data, pos)
			if !strings.Contains(timing, "-->") {
				return events, fault.New(fault.KindInvalid, "SRT expected timing line near offset %d", timingOffset).
					At(component, int64(timingOffset), int64(len(data)))
			}
		}

		startMS, endMS, err := parseSRTTiming(timing)
		if err != nil {
			return events, fault.Wrap(fault.KindInvalid, err, "parse SRT timing at offset %d", timingOffset).
				At(component, int64(timingOffset), int64(len(data)))
		}

		var textLines []string
		for pos < len(data) {
			text, next := readLine(data, pos)
			pos = next
			if strings.TrimSpace(text) == "" {
				break
			}
			textLines = append(textLines, strings.TrimRight(text, "\r"))
		}
		text := strings.Join(textLines, "\n")
		end := clock.NewTimestamp(endMS, 1000)
		events = append(events, Event{
			Track:        "subtitle",
			Kind:         "srt",
			Start:        clock.NewTimestamp(startMS, 1000),
			End:          &end,
			Text:         text,
			Digest:       DigestContent([]byte(text)),
			SourceOffset: int64(timingOffset),
		})
	}
	return events, nil
}

func readLine(data []byte, offset int) (string, int) {
	if offset < 0 || offset > len(data) {
		return "", len(data)
	}
	rel := bytes.IndexByte(data[offset:], '\n')
	if rel < 0 {
		return string(data[offset:]), len(data)
	}
	end := offset + rel
	return string(data[offset:end]), end + 1
}

func mustLine(data []byte, offset int) string {
	line, _ := readLine(data, offset)
	return line
}

func parseSRTTiming(line string) (clock.Ticks, clock.Ticks, error) {
	parts := strings.Split(line, "-->")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("missing -->")
	}
	start, err := srtTimeToMillis(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, err
	}
	end, err := srtTimeToMillis(strings.TrimSpace(strings.Split(parts[1], " ")[0]))
	if err != nil {
		return 0, 0, err
	}
	return clock.Ticks(start), clock.Ticks(end), nil
}

func srtTimeToMillis(v string) (int64, error) {
	// HH:MM:SS,mmm
	if len(v) < 12 || v[2] != ':' || v[5] != ':' || (v[8] != ',' && v[8] != '.') {
		return 0, fmt.Errorf("bad SRT timestamp %q", v)
	}
	h, err1 := strconv.Atoi(v[0:2])
	m, err2 := strconv.Atoi(v[3:5])
	s, err3 := strconv.Atoi(v[6:8])
	ms, err4 := strconv.Atoi(v[9:12])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return 0, fmt.Errorf("bad SRT timestamp %q", v)
	}
	return int64(h)*3_600_000 + int64(m)*60_000 + int64(s)*1000 + int64(ms), nil
}
