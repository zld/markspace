package manifest

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mediascan/internal/clock"
)

// Segment is one playlist entry on the unified timeline.
type Segment struct {
	URI               string `json:"uri"`
	DurationTicks     int64  `json:"duration_ticks"`
	Period            int    `json:"period"`
	Discontinuity     bool   `json:"discontinuity_before"`
	MapURI            string `json:"map_uri,omitempty"`
	GlobalStartTicks  int64  `json:"global_start_ticks"`
}

// Variant is one master-playlist stream representation.
type Variant struct {
	URI       string
	Bandwidth int
}

// HLS is a parsed media or master HLS playlist.
type HLS struct {
	Version        int
	TargetDuration int
	MapURI         string
	IsMaster       bool
	Segments       []Segment
	Variants       []Variant
}

// ParseHLS parses an M3U8 playlist. Fatal syntax problems return an error.
func ParseHLS(r io.Reader) (*HLS, error) {
	h := &HLS{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	sawHeader := false
	pendingDiscont := false
	period := 0
	var pendingInf int64 = -1
	var pendingVariant *Variant

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXTM3U") {
			sawHeader = true
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-VERSION:") {
			h.Version, _ = strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-VERSION:"))
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			h.TargetDuration, _ = strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
			continue
		}
		if line == "#EXT-X-DISCONTINUITY" {
			pendingDiscont = true
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
			if v := attrValue(line, "URI"); v != "" {
				h.MapURI = v
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			h.IsMaster = true
			bw := 0
			if v := attrValue(line, "BANDWIDTH"); v != "" {
				bw, _ = strconv.Atoi(v)
			}
			pendingVariant = &Variant{Bandwidth: bw}
			continue
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			rest := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.IndexByte(rest, ','); i >= 0 {
				rest = rest[:i]
			}
			d, err := SecondsToUnifiedTicks(rest)
			if err != nil {
				return nil, fmt.Errorf("hls: invalid EXTINF duration %q: %w", rest, err)
			}
			pendingInf = d
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		// URI line.
		if pendingVariant != nil {
			pendingVariant.URI = line
			h.Variants = append(h.Variants, *pendingVariant)
			pendingVariant = nil
			continue
		}
		if pendingInf < 0 {
			return nil, fmt.Errorf("hls: segment %q without preceding EXTINF", line)
		}
		if pendingDiscont && len(h.Segments) > 0 {
			period++
		}
		h.Segments = append(h.Segments, Segment{
			URI:           line,
			DurationTicks: pendingInf,
			Period:        period,
			Discontinuity: pendingDiscont,
			MapURI:        h.MapURI,
		})
		pendingDiscont = false
		pendingInf = -1
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !sawHeader {
		return nil, fmt.Errorf("hls: missing #EXTM3U header")
	}
	if !h.IsMaster && len(h.Segments) == 0 {
		return nil, fmt.Errorf("hls: media playlist contains no segments")
	}
	AssignHLSTimeline(h)
	return h, nil
}

// AssignHLSTimeline lays segments on an ordered unified axis while keeping
// discontinuity period grouping intact.
func AssignHLSTimeline(h *HLS) {
	var cur int64
	for i := range h.Segments {
		h.Segments[i].GlobalStartTicks = cur
		cur += h.Segments[i].DurationTicks
	}
}

// SecondsToUnifiedTicks parses an EXTINF decimal-second value exactly into
// unified ticks, rounding the sub-microsecond remainder.
func SecondsToUnifiedTicks(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	secs, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, err
	}
	// Pad/truncate fractional seconds to 6 digits (microseconds), rounding.
	for len(fracPart) < 6 {
		fracPart += "0"
	}
	frac6 := fracPart[:6]
	micros, err := strconv.ParseInt(frac6, 10, 64)
	if err != nil {
		return 0, err
	}
	if len(fracPart) > 6 && fracPart[6] >= '5' {
		micros++
	}
	v := secs*int64(clock.UnifiedHz) + micros
	if neg {
		v = -v
	}
	return v, nil
}

// attrValue extracts an attribute value, supporting quoted and unquoted.
func attrValue(line, key string) string {
	marker := key + "="
	idx := strings.Index(line, marker)
	if idx < 0 {
		return ""
	}
	v := line[idx+len(marker):]
	if strings.HasPrefix(v, "\"") {
		end := strings.IndexByte(v[1:], '"')
		if end < 0 {
			return ""
		}
		return v[1 : 1+end]
	}
	if comma := strings.IndexByte(v, ','); comma >= 0 {
		v = v[:comma]
	}
	return strings.TrimSpace(v)
}
