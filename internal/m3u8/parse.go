package m3u8

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParsedMedia is a media playlist reconstructed from text. The verify path
// reparses published files through this type rather than trusting in-memory
// state.
type ParsedMedia struct {
	TargetDuration int
	MediaSequence  int
	Closed         bool
	Segments       []ParsedSegment
}

// ParsedSegment is one reparsed entry.
type ParsedSegment struct {
	URI           string
	Duration      float64
	Discontinuity bool
}

// ParseMedia parses a media playlist from r.
func ParseMedia(r io.Reader) (*ParsedMedia, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	out := &ParsedMedia{MediaSequence: 0}
	sawHeader := false
	pendingDisc := false
	pendingInf := false
	var pendingDur float64
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "#EXTM3U" {
			sawHeader = true
			continue
		}
		switch {
		case line == "#EXT-X-ENDLIST":
			out.Closed = true
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			v, err := parseTagInt(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			out.TargetDuration = v
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			v, err := parseTagInt(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			out.MediaSequence = v
		case line == "#EXT-X-DISCONTINUITY":
			pendingDisc = true
		case strings.HasPrefix(line, "#EXTINF:"):
			d, err := strconv.ParseFloat(strings.TrimPrefix(strings.SplitN(line, ",", 2)[0], "#EXTINF:"), 64)
			if err != nil {
				return nil, fmt.Errorf("line %d: bad EXTINF: %w", lineNo, err)
			}
			pendingDur = d
			pendingInf = true
		case strings.HasPrefix(line, "#"):
			// Unknown/unsupported tags are tolerated.
		default:
			if !pendingInf {
				return nil, fmt.Errorf("line %d: URI %q without EXTINF", lineNo, line)
			}
			out.Segments = append(out.Segments, ParsedSegment{
				URI: line, Duration: pendingDur, Discontinuity: pendingDisc,
			})
			pendingDisc, pendingInf = false, false
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !sawHeader {
		return nil, fmt.Errorf("not an M3U8 playlist: missing #EXTM3U")
	}
	// Reconstruct media sequence numbers on entries for callers.
	for i := range out.Segments {
		_ = i
	}
	return out, nil
}

// MediaSeqOf returns the EXT-X-MEDIA-SEQUENCE value that segment i would carry.
func (p *ParsedMedia) MediaSeqOf(i int) int { return p.MediaSequence + i }

func parseTagInt(line string) (int, error) {
	idx := strings.IndexByte(line, ':')
	return strconv.Atoi(strings.TrimSpace(line[idx+1:]))
}

// ParseMaster returns variant URIs in declared order from a master playlist.
func ParseMaster(r io.Reader) ([]Variant, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out []Variant
	var pending *Variant
	sawHeader := false
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "#EXTM3U" {
			sawHeader = true
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			v, err := parseStreamInf(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			pending = &v
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if pending == nil {
			return nil, fmt.Errorf("line %d: variant URI without STREAM-INF", lineNo)
		}
		pending.URI = line
		out = append(out, *pending)
		pending = nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !sawHeader {
		return nil, fmt.Errorf("not an M3U8 playlist: missing #EXTM3U")
	}
	return out, nil
}

func parseStreamInf(line string) (Variant, error) {
	var v Variant
	attrs := strings.TrimPrefix(line, "#EXT-X-STREAM-INF:")
	for _, part := range splitAttrs(attrs) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		switch key {
		case "BANDWIDTH":
			n, err := strconv.Atoi(val)
			if err != nil {
				return v, fmt.Errorf("bad BANDWIDTH %q", val)
			}
			v.Bandwidth = n
		case "AVERAGE-BANDWIDTH":
			n, _ := strconv.Atoi(val)
			v.AverageBandwidth = n
		case "NAME":
			v.Name = val
		case "CODECS":
			v.Codecs = val
		}
	}
	if v.Bandwidth <= 0 {
		return v, fmt.Errorf("STREAM-INF missing BANDWIDTH")
	}
	return v, nil
}

// splitAttrs splits on commas that are not inside a quoted value.
func splitAttrs(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// RoundTrip is a test-friendly helper returning the reparsed media playlist.
func RoundTrip(p MediaPlaylist) (*ParsedMedia, error) {
	return ParseMedia(bytes.NewReader(p.Bytes()))
}
