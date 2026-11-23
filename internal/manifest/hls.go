package manifest

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"archivecheck/internal/model"
)

// utf8BOM is the UTF-8 byte order mark that HLS permits at the start of a
// playlist.
const utf8BOM = "\ufeff"

// hlsSegment is one EXTINF entry before timeline assignment.
type hlsSegment struct {
	line          int
	duration      model.Rational
	title         string
	uri           string
	uriLine       int
	byteRange     *Range
	rangeExplicit bool
	discontinuity bool
	gap           bool
}

// hlsPlaylist is the raw parse result.
type hlsPlaylist struct {
	file           string
	mediaSequence  int64
	targetDuration model.Rational
	hasTarget      bool
	playlistType   string
	endList        bool
	segments       []hlsSegment
}

// hlsPending is the in-progress segment between an EXTINF tag and its URI.
type hlsPending struct {
	hasDuration   bool
	duration      model.Rational
	title         string
	line          int
	byteRange     *Range
	rangeExplicit bool
	discontinuity bool
	gap           bool
}

func (p *hlsPending) reset() { *p = hlsPending{} }

func (p *hlsPending) taken() bool {
	return p.hasDuration || p.byteRange != nil || p.discontinuity || p.gap
}

// ParseHLS parses an HLS media playlist. It rejects master playlists and
// remote URIs, and never consults the network.
func ParseHLS(name string, r io.Reader, opts Options) (*Plan, error) {
	pl, v := parseHLSRaw(name, r)
	if v.faults.Len() > 0 {
		return nil, v.Err()
	}
	return buildHLSTimeline(pl, opts, v)
}

func parseHLSRaw(name string, r io.Reader) (*hlsPlaylist, *Validator) {
	pl := &hlsPlaylist{file: name}
	v := NewValidator(name)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	sawHeader := false
	var pend hlsPending
	for sc.Scan() {
		lineNo++
		v.At(lineNo, 1)
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), utf8BOM))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if !strings.HasPrefix(line, "#EXT") {
				continue // ordinary comment
			}
			tag, value, _ := strings.Cut(line, ":")
			switch tag {
			case "#EXTM3U":
				if sawHeader {
					v.fail(model.CodeManifestSyntax, "#EXTM3U repeated")
				}
				sawHeader = true
				continue
			case "#EXT-X-MEDIA-SEQUENCE":
				n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
				if err != nil || n < 0 {
					v.fail(model.CodeManifestSyntax, "invalid EXT-X-MEDIA-SEQUENCE %q", value)
					continue
				}
				pl.mediaSequence = n
			case "#EXT-X-TARGETDURATION":
				d, err := ParseDecimalSeconds(value)
				if err != nil {
					v.fail(model.CodeManifestSyntax, "invalid EXT-X-TARGETDURATION %q: %v", value, err)
					continue
				}
				pl.targetDuration, pl.hasTarget = d, true
			case "#EXT-X-PLAYLIST-TYPE":
				pl.playlistType = strings.TrimSpace(value)
			case "#EXT-X-ENDLIST":
				pl.endList = true
			case "#EXT-X-STREAM-INF", "#EXT-X-MEDIA", "#EXT-X-I-FRAME-STREAM-INF":
				v.fail(model.CodeManifestSyntax, "master playlist tag %s is not a media playlist", tag)
			case "#EXT-X-BYTERANGE":
				if pend.byteRange != nil {
					v.fail(model.CodeManifestBoundary, "EXT-X-BYTERANGE repeated before a segment URI")
					continue
				}
				br, explicit, err := parseByteRange(value)
				if err != nil {
					v.fail(model.CodeManifestBadRange, "invalid EXT-X-BYTERANGE %q: %v", value, err)
					continue
				}
				pend.byteRange, pend.rangeExplicit = &br, explicit
			case "#EXT-X-DISCONTINUITY":
				pend.discontinuity = true
			case "#EXT-X-GAP":
				pend.gap = true
			case "#EXTINF":
				if pend.hasDuration {
					v.fail(model.CodeManifestSyntax, "EXTINF repeated before a segment URI")
					continue
				}
				d, title, err := parseExtInf(value)
				if err != nil {
					v.fail(model.CodeManifestMissingField, "invalid EXTINF %q: %v", value, err)
					continue
				}
				pend.hasDuration, pend.duration, pend.title = true, d, title
				pend.line = lineNo
			}
			continue
		}
		if !sawHeader {
			v.fail(model.CodeManifestSyntax, "first non-empty line must be #EXTM3U")
			sawHeader = true
		}
		if !pend.hasDuration {
			v.fail(model.CodeManifestBoundary, "segment URI %q has no preceding EXTINF duration", line)
			pend.reset()
			continue
		}
		if !pend.gap {
			if err := ValidateSimpleName(line); err != nil {
				v.At(lineNo, 1)
				v.fail(model.CodeManifestBadRange, "segment URI %q rejected: %v", line, err)
				pend.reset()
				continue
			}
		}
		pl.segments = append(pl.segments, hlsSegment{
			line:          pend.line,
			duration:      pend.duration,
			title:         pend.title,
			uri:           line,
			uriLine:       lineNo,
			byteRange:     pend.byteRange,
			rangeExplicit: pend.rangeExplicit,
			discontinuity: pend.discontinuity,
			gap:           pend.gap,
		})
		pend.reset()
	}
	if err := sc.Err(); err != nil {
		v.fail(model.CodeManifestSyntax, "read error: %v", err)
	}
	if pend.taken() {
		v.At(pend.line, 1)
		v.fail(model.CodeManifestBoundary, "EXTINF/BYTERANGE has no segment URI before end of playlist")
	}
	if !sawHeader {
		v.fail(model.CodeManifestSyntax, "missing #EXTM3U header")
	}
	if len(pl.segments) == 0 {
		v.fail(model.CodeManifestBoundary, "media playlist contains no segments")
	}
	return pl, v
}

func parseExtInf(value string) (model.Rational, string, error) {
	durText, title, ok := strings.Cut(value, ",")
	if !ok {
		return model.Rational{}, "", fmt.Errorf("missing comma")
	}
	if strings.TrimSpace(durText) == "" {
		return model.Rational{}, "", fmt.Errorf("missing duration")
	}
	d, err := ParseDecimalSeconds(durText)
	return d, title, err
}

// parseByteRange parses "<length>[@<offset>]" and reports whether the offset
// was explicit.
func parseByteRange(value string) (Range, bool, error) {
	text := strings.TrimSpace(value)
	lenText, offText, explicit := strings.Cut(text, "@")
	n, err := strconv.ParseInt(strings.TrimSpace(lenText), 10, 64)
	if err != nil || n <= 0 {
		return Range{}, false, fmt.Errorf("length must be a positive integer")
	}
	if !explicit {
		return Range{Start: 0, End: n}, false, nil
	}
	off, err := strconv.ParseInt(strings.TrimSpace(offText), 10, 64)
	if err != nil || off < 0 {
		return Range{}, false, fmt.Errorf("offset must be a non-negative integer")
	}
	total, ok := addRange(off, n)
	if !ok {
		return Range{}, false, fmt.Errorf("range overflows int64")
	}
	if total <= off {
		return Range{}, false, fmt.Errorf("empty or inverted range")
	}
	return Range{Start: off, End: total}, true, nil
}

func addRange(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > model.MaxTicks || b > model.MaxTicks-a {
		return 0, false
	}
	return a + b, true
}
