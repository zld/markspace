package manifest

import (
	"fmt"
	"strconv"
	"strings"

	"mediamux/media"
)

// ParseHLS parses an HLS media playlist. It reports the target duration,
// discontinuity flags and each segment's declared duration as an exact ratio.
// The returned Document already has every segment placed on the unified
// integer timeline, with any sub-tick drift accumulated in Alignment.
func ParseHLS(src string) (*Document, error) {
	if !strings.HasPrefix(strings.TrimSpace(src), "#EXTM3U") {
		return nil, ErrLine(media.CodeInput, 0, "HLS playlist missing #EXTM3U header")
	}
	b := newBuilder(KindHLS, 0)
	var (
		target     media.Ratio
		targetSeen bool
		haveDur    bool
		dur        media.Ratio
		durOffset  int64
		disc       bool
		initURI    string
		bps        int64
	)
	lineNo := 0
	offset := int64(0)
	for _, raw := range strings.SplitAfter(src, "\n") {
		lineNo++
		lineStart := offset
		offset += int64(len(raw))
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			body := strings.TrimPrefix(line, "#")
			tag, value, _ := strings.Cut(body, ":")
			switch tag {
			case "EXTM3U":
				if lineNo != 1 {
					return nil, ErrLine(media.CodeManifestDrift, lineStart, "#EXTM3U must be the first line")
				}
			case "EXT-X-TARGETDURATION":
				d, err := parsePositiveDecimal(value)
				if err != nil {
					return nil, ErrLine(media.CodeManifestDrift, lineStart, err.Error())
				}
				target, targetSeen = d, true
			case "EXT-X-MEDIA-SEQUENCE":
				n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
				if err != nil || n < 0 {
					return nil, ErrLine(media.CodeManifestDrift, lineStart, "invalid media sequence")
				}
				b.seq = n
			case "EXT-X-DISCONTINUITY":
				disc = true
			case "EXTINF":
				durPart, _, _ := strings.Cut(value, ",")
				d, err := parsePositiveDecimal(durPart)
				if err != nil {
					return nil, ErrLine(media.CodeManifestDrift, lineStart, err.Error())
				}
				dur, durOffset, haveDur = d, lineStart, true
			case "EXT-X-MAP":
				if uri, ok := attrValue(value, "URI"); ok {
					initURI = uri
				}
			case "EXT-X-BITRATE":
				kb, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
				if err != nil || kb < 0 {
					return nil, ErrLine(media.CodeManifestDrift, lineStart, "invalid bitrate")
				}
				bps = kb * 1000
			default:
				// Unknown tags are ignored for forward compatibility.
			}
			continue
		}
		// A non-comment line is a segment URI and must follow #EXTINF.
		if !haveDur {
			return nil, ErrLine(media.CodeManifestDrift, lineStart,
				fmt.Sprintf("segment %q has no preceding #EXTINF", line))
		}
		srcTicks, _, err := floorTicks(dur, OutputTimescale)
		if err != nil {
			return nil, ErrLine(media.CodeManifestDrift, durOffset, err.Error())
		}
		if err := b.add(line, initURI, dur, disc, srcTicks, OutputTimescale, durOffset); err != nil {
			return nil, err
		}
		haveDur, disc = false, false
	}
	if haveDur {
		return nil, ErrLine(media.CodeManifestDrift, durOffset, "trailing #EXTINF without segment URI")
	}
	if len(b.segments) == 0 {
		return nil, ErrLine(media.CodeInput, 0, "HLS playlist has no media segments")
	}
	if !targetSeen {
		return nil, ErrLine(media.CodeManifestDrift, 0, "HLS playlist missing #EXT-X-TARGETDURATION")
	}
	return b.finish(target, bps), nil
}

// parsePositiveDecimal parses a plain non-negative decimal duration.
func parsePositiveDecimal(s string) (media.Ratio, error) {
	r, err := media.ParseDecimalSeconds(s)
	if err != nil {
		return media.Ratio{}, fmt.Errorf("invalid duration %q", strings.TrimSpace(s))
	}
	return r, nil
}

// attrValue extracts a quoted attribute from an HLS attribute list.
func attrValue(list, key string) (string, bool) {
	for _, part := range strings.Split(list, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), `"`), true
	}
	return "", false
}
