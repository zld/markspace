package manifest

import (
	"fmt"
	"strconv"
	"strings"
)

// Parse reads a generated media playlist back into the model. It is
// deliberately strict: unknown tags, malformed EXTINF lines, missing ENDLIST
// and duplicate file names are all errors.
func Parse(data []byte) (Playlist, error) {
	var p Playlist
	var haveHeader, haveEnd bool
	var pendingMS int64
	var pendingSet bool
	seen := make(map[string]bool)
	lines := strings.Split(string(data), "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		switch {
		case line == "#EXTM3U":
			haveHeader = true
		case line == "#EXT-X-ENDLIST":
			haveEnd = true
		case strings.HasPrefix(line, "#EXT-X-VERSION:"):
			if _, err := fmt.Sscanf(line, "#EXT-X-VERSION:%d", &p.Version); err != nil {
				return p, fmt.Errorf("line %d: malformed version tag", i+1)
			}
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			if _, err := fmt.Sscanf(line, "#EXT-X-TARGETDURATION:%d", &p.TargetDuration); err != nil {
				return p, fmt.Errorf("line %d: malformed target duration", i+1)
			}
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			if _, err := fmt.Sscanf(line, "#EXT-X-MEDIA-SEQUENCE:%d", &p.MediaSequence); err != nil {
				return p, fmt.Errorf("line %d: malformed media sequence", i+1)
			}
		case line == "#EXT-X-PLAYLIST-TYPE:VOD", line == "#EXT-X-INDEPENDENT-SEGMENTS":
			// Accepted; both are emitted for every generated playlist.
		case strings.HasPrefix(line, "#EXTINF:"):
			if pendingSet {
				return p, fmt.Errorf("line %d: EXTINF without a following segment", i+1)
			}
			ms, err := ParseEXTINF(strings.TrimPrefix(line, "#EXTINF:"))
			if err != nil {
				return p, fmt.Errorf("line %d: %w", i+1, err)
			}
			pendingMS, pendingSet = ms, true
		case strings.HasPrefix(line, "#"):
			return p, fmt.Errorf("line %d: unexpected tag %q", i+1, line)
		default:
			if !pendingSet {
				return p, fmt.Errorf("line %d: segment %q without EXTINF", i+1, line)
			}
			if seen[line] {
				return p, fmt.Errorf("line %d: duplicate segment %q", i+1, line)
			}
			seen[line] = true
			p.Segments = append(p.Segments, PlaylistSegment{
				ID:         p.MediaSequence + len(p.Segments),
				FileName:   line,
				DurationMS: pendingMS,
			})
			pendingSet = false
		}
	}
	if !haveHeader {
		return p, fmt.Errorf("missing #EXTM3U header")
	}
	if pendingSet {
		return p, fmt.Errorf("EXTINF without a segment at end of file")
	}
	if !haveEnd {
		return p, fmt.Errorf("missing #EXT-X-ENDLIST")
	}
	if len(p.Segments) > 0 && p.TargetDuration != TargetDurationSeconds(p.Segments) {
		return p, fmt.Errorf("TARGETDURATION %d does not match segment durations (%d)",
			p.TargetDuration, TargetDurationSeconds(p.Segments))
	}
	return p, nil
}

// ParseEXTINF parses the duration part of an EXTINF line, which must carry
// exactly three decimal places and a trailing comma (title omitted).
func ParseEXTINF(v string) (int64, error) {
	v = strings.TrimSuffix(strings.TrimSpace(v), ",")
	dot := strings.IndexByte(v, '.')
	if dot < 0 {
		return 0, fmt.Errorf("EXTINF %q lacks millisecond precision", v)
	}
	whole, frac := v[:dot], v[dot+1:]
	if len(frac) != 3 {
		return 0, fmt.Errorf("EXTINF %q must have exactly 3 decimal places", v)
	}
	if !allDigits(whole) || !allDigits(frac) {
		return 0, fmt.Errorf("EXTINF %q must contain only digits around the decimal point", v)
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("EXTINF %q has invalid seconds", v)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("EXTINF %q has invalid milliseconds", v)
	}
	return w*1000 + f, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
