package manifest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"streamcheck/internal/model"
)

// ErrParse marks a structural manifest error. Callers wrap it so they can
// distinguish "the playlist is broken" from "the disk disagrees".
var ErrParse = errors.New("manifest parse error")

// Parse reads an M3U8 media playlist and returns its normalized form. It is
// the single parser used both by verification and by tests of generated
// output; durations and URIs therefore travel through the same rules that
// Build applied.
func Parse(r io.Reader, rate int) (model.Manifest, error) {
	m := model.Manifest{Rate: rate}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	first := true
	var pending *int64
	sawTarget := false
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			if line != "#EXTM3U" {
				return m, fmt.Errorf("%w: line %d: missing #EXTM3U header", ErrParse, lineNo)
			}
			first = false
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			if pending != nil {
				return m, fmt.Errorf("%w: line %d: EXTINF without URI", ErrParse, lineNo)
			}
			value := strings.TrimPrefix(line, "#EXTINF:")
			// EXTINF is "<duration>[<title>]", terminated by the first comma.
			durationText, _, _ := strings.Cut(value, ",")
			ms, err := ParseDurationMS(durationText)
			if err != nil {
				return m, fmt.Errorf("%w: line %d: %v", ErrParse, lineNo, err)
			}
			if ms <= 0 {
				return m, fmt.Errorf("%w: line %d: non-positive duration", ErrParse, lineNo)
			}
			pending = &ms
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			value := strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")
			secs, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil || secs <= 0 {
				return m, fmt.Errorf("%w: line %d: invalid target duration %q", ErrParse, lineNo, value)
			}
			m.TargetDurationMS = secs * 1000
			sawTarget = true
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			value := strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")
			seq, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || seq < 0 {
				return m, fmt.Errorf("%w: line %d: invalid media sequence %q", ErrParse, lineNo, value)
			}
			m.MediaSequence = seq
		case line == "#EXT-X-ENDLIST":
			m.EndList = true
		case strings.HasPrefix(line, "#"):
			// Unknown tags (PLAYLIST-TYPE, VERSION) are tolerated.
			continue
		default:
			if pending == nil {
				return m, fmt.Errorf("%w: line %d: URI without EXTINF", ErrParse, lineNo)
			}
			uri, err := NormalizeURI(line)
			if err != nil {
				return m, fmt.Errorf("%w: line %d: %v", ErrParse, lineNo, err)
			}
			m.Segments = append(m.Segments, model.ManifestEntry{DurationMS: *pending, URI: uri})
			pending = nil
		}
	}
	if err := sc.Err(); err != nil {
		return m, fmt.Errorf("%w: read: %v", ErrParse, err)
	}
	if first {
		return m, fmt.Errorf("%w: empty manifest", ErrParse)
	}
	if pending != nil {
		return m, fmt.Errorf("%w: trailing EXTINF without URI", ErrParse)
	}
	if !sawTarget {
		return m, fmt.Errorf("%w: missing EXT-X-TARGETDURATION", ErrParse)
	}
	if len(m.Segments) == 0 {
		return m, fmt.Errorf("%w: manifest declares no segments", ErrParse)
	}
	if len(m.Segments) > model.MaxSegments {
		return m, fmt.Errorf("%w: segment count %d exceeds limit %d", ErrParse, len(m.Segments), model.MaxSegments)
	}
	var longest int64
	for _, entry := range m.Segments {
		if entry.DurationMS > longest {
			longest = entry.DurationMS
		}
	}
	if want := TargetDurationSeconds(longest) * 1000; m.TargetDurationMS < want {
		return m, fmt.Errorf("%w: target duration %dms is shorter than the longest segment (%dms)",
			ErrParse, m.TargetDurationMS, longest)
	}
	return m, nil
}

// ParseBytes is a convenience wrapper around Parse for in-memory playlists.
func ParseBytes(data []byte, rate int) (model.Manifest, error) {
	return Parse(strings.NewReader(string(data)), rate)
}
