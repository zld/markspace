package manifest

import (
	"bytes"
	"os"
	"strconv"
	"strings"

	"archivecheck/internal"
)

// HLSSegment is one URI plus its integer-nanosecond presentation duration.
type HLSSegment struct {
	Index          int    `json:"index"`
	URI            string `json:"uri"`
	StartNS        int64  `json:"start_ns"`
	DurationNS     int64  `json:"duration_ns"`
	Discontinuity  bool   `json:"discontinuity"`
	EXTINFLine     int    `json:"extinf_line"`
	URIOffset      int    `json:"uri_offset"`
}

// HLSManifest contains the normalized media playlist view needed by recovery.
type HLSManifest struct {
	Source          string       `json:"source"`
	TargetDurationNS int64        `json:"target_duration_ns"`
	Segments        []HLSSegment  `json:"segments"`
	Discontinuities []int         `json:"discontinuity_segment_indexes"`
}

// ParseHLS parses a media playlist from memory. Offsets in faults are zero-based
// byte offsets of the offending line.
func ParseHLS(data []byte, source string) (*HLSManifest, error) {
	m := &HLSManifest{Source: source}
	var (
		pendingDiscontinuity bool
		cursor               int64
		lineNo               int
	)

	for off := 0; off <= len(data); {
		lineStart := off
		nl := bytes.IndexByte(data[off:], '\n')
		var line []byte
		if nl < 0 {
			line = data[off:]
			off = len(data) + 1
		} else {
			line = data[off : off+nl]
			off += nl + 1
		}
		lineNo++
		line = bytes.TrimSuffix(line, []byte{'\r'})
		text := strings.TrimSpace(string(line))
		if text == "" {
			if off > len(data) {
				break
			}
			continue
		}

		if strings.HasPrefix(text, "#EXT-X-TARGETDURATION:") {
			v := strings.TrimPrefix(text, "#EXT-X-TARGETDURATION:")
			secs, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil || secs <= 0 {
				return nil, internal.NewFault("hls.parse", source, lineStart, "invalid TARGETDURATION %q", v)
			}
			m.TargetDurationNS = secs * int64(internal.NSPerSecond)
		}

		if text == "#EXT-X-DISCONTINUITY" {
			pendingDiscontinuity = true
		}

		if strings.HasPrefix(text, "#EXTINF:") {
			value := strings.TrimPrefix(text, "#EXTINF:")
			if comma := strings.IndexByte(value, ','); comma >= 0 {
				value = value[:comma]
			}
			d, err := internal.ParseDecimalSeconds(value)
			if err != nil || d <= 0 {
				return nil, internal.WrapFault("hls.parse", source, lineStart,
					internal.NewFault("hls.parse", source, lineStart, "invalid EXTINF duration %q", value))
			}

			extinfLine := lineNo
			uri, uriOffset, nextLine, err := nextHLSURI(data, off, &lineNo)
			if err != nil {
				return nil, internal.WrapFault("hls.parse", source, lineStart, err)
			}
			seg := HLSSegment{
				Index:         len(m.Segments),
				URI:           uri,
				StartNS:       cursor,
				DurationNS:    int64(d),
				Discontinuity: pendingDiscontinuity,
				EXTINFLine:    extinfLine,
				URIOffset:     uriOffset,
			}
			if seg.Discontinuity {
				m.Discontinuities = append(m.Discontinuities, seg.Index)
			}
			m.Segments = append(m.Segments, seg)
			cursor += int64(d)
			pendingDiscontinuity = false
			off = nextLine
		}

		if off > len(data) {
			break
		}
	}

	if len(m.Segments) == 0 {
		return nil, internal.NewFault("hls.parse", source, 0, "playlist contains no EXTINF segments")
	}
	return m, nil
}

// ParseHLSFile reads and parses a local playlist.
func ParseHLSFile(path string) (*HLSManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, internal.WrapFault("hls.read", path, 0, err)
	}
	m, err := ParseHLS(data, path)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func nextHLSURI(data []byte, start int, lineNo *int) (uri string, offset int, next int, err error) {
	off := start
	for off <= len(data) {
		lineStart := off
		nl := bytes.IndexByte(data[off:], '\n')
		var line []byte
		if nl < 0 {
			line = data[off:]
			next = len(data) + 1
		} else {
			line = data[off : off+nl]
			next = off + nl + 1
		}
		*lineNo++
		line = bytes.TrimSuffix(line, []byte{'\r'})
		text := strings.TrimSpace(string(line))
		if text != "" && !strings.HasPrefix(text, "#") {
			return text, lineStart, next, nil
		}
		if nl < 0 {
			break
		}
		off = next
	}
	return "", 0, 0, internal.NewFault("hls.parse", "", start, "EXTINF has no segment URI")
}
