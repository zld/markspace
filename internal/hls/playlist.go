// Package hls parses media playlists (RFC 8216). EXT-X-DISCONTINUITY
// does not reset the media timeline: segments keep appending onto one
// accumulated integer-tick timeline. Every parse failure carries the
// raw byte offset of the offending line.
package hls

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"mediascan/internal/clock"
	"mediascan/internal/errs"
)

// Segment is one media segment entry in a playlist.
type Segment struct {
	Index       int
	URI         string
	DurationMS  int64 // EXTINF duration in whole milliseconds
	LineOffset  int64 // byte offset of the EXTINF line
	Discontinuity bool // true when preceded by EXT-X-DISCONTINUITY
	ByteRange   *Range
}

// Range is a half-open byte interval [Start, End).
type Range struct {
	Start int64
	End   int64
}

// Playlist is a parsed media playlist.
type Playlist struct {
	Source       string
	TargetDurMS  int64
	Version      int
	Independent  bool
	Segments     []Segment
	Endlist      bool
	raw          []byte
	segStarts    []int64 // continuous-timeline start of each segment (ms)
	discOffsets  []int64 // byte offsets of EXT-X-DISCONTINUITY tags
	// Timeline positions each segment on a shared 1 ms scale. Discontinuity
	// boundaries are visible as ordinary adjacent ranges.
	timeline     *clock.Timeline
}

// Raw returns the exact playlist bytes.
func (p *Playlist) Raw() []byte { return p.raw }

// TimelineRange returns the half-open millisecond range occupied by the
// segment at index i on the continuous media timeline.
func (p *Playlist) TimelineRange(i int) (clock.Range, error) {
	if i < 0 || i >= len(p.Segments) {
		return clock.Range{}, errs.New(errs.KindSegment, p.Source, errs.Unknown,
			int64(i), 0, "segment index %d out of range", i)
	}
	// The timeline accumulator keyed by track 1 is driven in Parse; here
	// we reconstruct ranges deterministically from stored durations.
	return clock.Range{
		Start: p.segStarts[i],
		End:   p.segStarts[i] + p.Segments[i].DurationMS,
	}, nil
}

// DiscontinuityOffsets returns the byte offsets of every
// EXT-X-DISCONTINUITY tag, in playlist order.
func (p *Playlist) DiscontinuityOffsets() []int64 {
	out := make([]int64, 0, 4)
	for i, off := range p.discOffsets {
		_ = i
		out = append(out, off)
	}
	return out
}

// TotalDurationMS sums all EXTINF durations on the continuous timeline.
func (p *Playlist) TotalDurationMS() int64 {
	if len(p.segStarts) == 0 {
		return 0
	}
	last := p.Segments[len(p.Segments)-1]
	return p.segStarts[len(p.segStarts)-1] + last.DurationMS
}

// Parse reads a media playlist and validates its structural constraints.
func Parse(source string, raw []byte) (*Playlist, error) {
	p := &Playlist{Source: source, raw: append([]byte(nil), raw...), Version: 1}
	tl, err := clock.NewTimeline(1000) // milliseconds
	if err != nil {
		return nil, err
	}
	p.timeline = tl

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var offset int64
	lineNo := 0
	var pendingDur *int64
	var pendingDisc bool
	var pendingExtinfOff int64
	var pendingRange *Range

	flush := func(uri string, uriOff int64) error {
		if pendingDur == nil {
			return errs.New(errs.KindManifest, source, uriOff, int64(len(p.Segments)), 0,
				"segment URI %q has no preceding EXTINF tag", uri)
		}
		rng, ferr := tl.Append(1, *pendingDur, 1000)
		if ferr != nil {
			return errs.New(errs.KindTimeline, source, pendingExtinfOff,
				int64(len(p.Segments)), 0, "timeline append failed: %v", ferr)
		}
		p.segStarts = append(p.segStarts, rng.Start)
		p.Segments = append(p.Segments, Segment{
			Index:         len(p.Segments),
			URI:           uri,
			DurationMS:    *pendingDur,
			LineOffset:    pendingExtinfOff,
			Discontinuity: pendingDisc,
			ByteRange:     pendingRange,
		})
		pendingDur, pendingDisc, pendingRange = nil, false, nil
		return nil
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		lineOff := offset
		offset += int64(len(line)) + 1 // account for the trailing '\n'
		lineNo++
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" || trimmed == "#EXTM3U" {
			continue
		}
		if strings.HasPrefix(trimmed, "##") {
			return nil, errs.New(errs.KindManifest, source, lineOff, errs.Unknown, 0,
				"line %d: malformed tag %q", lineNo, trimmed)
		}
		if !strings.HasPrefix(trimmed, "#") {
			if err := flush(trimmed, lineOff); err != nil {
				return nil, err
			}
			continue
		}
		tag, value, _ := strings.Cut(trimmed, ":")
		switch tag {
		case "#EXT-X-VERSION":
			v, perr := strconv.Atoi(strings.TrimSpace(value))
			if perr != nil || v < 0 {
				return nil, errs.New(errs.KindManifest, source, lineOff, errs.Unknown, 0,
					"line %d: invalid EXT-X-VERSION %q", lineNo, value)
			}
			p.Version = v
		case "#EXT-X-TARGETDURATION":
			v, perr := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if perr != nil || v <= 0 {
				return nil, errs.New(errs.KindManifest, source, lineOff, errs.Unknown, 0,
					"line %d: invalid EXT-X-TARGETDURATION %q", lineNo, value)
			}
			p.TargetDurMS = v * 1000
		case "#EXT-X-INDEPENDENT-SEGMENTS":
			p.Independent = true
		case "#EXT-X-ENDLIST":
			p.Endlist = true
		case "#EXT-X-DISCONTINUITY":
			pendingDisc = true
			p.discOffsets = append(p.discOffsets, lineOff)
		case "#EXT-X-BYTERANGE":
			br, perr := parseByteRange(strings.TrimSpace(value), source, lineOff, lineNo)
			if perr != nil {
				return nil, perr
			}
			pendingRange = br
		case "#EXTINF":
			dur, perr := parseExtinf(value, source, lineOff, lineNo)
			if perr != nil {
				return nil, perr
			}
			pendingDur = &dur
			pendingExtinfOff = lineOff
		case "#EXT-X-MEDIA-SEQUENCE", "#EXT-X-PLAYLIST-TYPE",
			"#EXT-X-KEY", "#EXT-X-MAP", "#EXT-X-PROGRAM-DATE-TIME",
			"#EXT-X-ALLOW-CACHE":
			// Accepted metadata tags; validation scope does not need them.
		default:
			return nil, errs.New(errs.KindManifest, source, lineOff, errs.Unknown, 0,
				"line %d: unparseable or unsupported tag %q", lineNo, tag)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, errs.New(errs.KindIO, source, errs.Unknown, errs.Unknown, 0,
			"playlist read failed: %v", err)
	}
	if pendingDur != nil {
		return nil, errs.New(errs.KindManifest, source, offset,
			int64(len(p.Segments)), 0, "EXTINF without a following segment URI")
	}
	if len(p.Segments) == 0 {
		return nil, errs.New(errs.KindManifest, source, 0, errs.Unknown, 0,
			"media playlist contains no segments")
	}
	return p, nil
}

// parseExtinf parses "<duration>,<title>"; durations are decimal seconds
// converted to integer milliseconds with exact digit arithmetic (no float).
func parseExtinf(value, source string, off int64, lineNo int) (int64, error) {
	durText, _, _ := strings.Cut(value, ",")
	durText = strings.TrimSpace(durText)
	if durText == "" {
		return 0, errs.New(errs.KindManifest, source, off, errs.Unknown, 0,
			"line %d: EXTINF missing duration", lineNo)
	}
	neg := false
	if strings.HasPrefix(durText, "-") {
		neg = true
		durText = durText[1:]
	}
	whole, frac, hasDot := strings.Cut(durText, ".")
	if whole == "" || (!hasDot && frac != "") {
		return 0, errs.New(errs.KindManifest, source, off, errs.Unknown, 0,
			"line %d: invalid EXTINF duration %q", lineNo, durText)
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, errs.New(errs.KindManifest, source, off, errs.Unknown, 0,
			"line %d: invalid EXTINF duration %q", lineNo, durText)
	}
	ms := w * 1000
	if hasDot {
		if frac == "" {
			return 0, errs.New(errs.KindManifest, source, off, errs.Unknown, 0,
				"line %d: invalid EXTINF duration %q", lineNo, durText)
		}
		f, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, errs.New(errs.KindManifest, source, off, errs.Unknown, 0,
				"line %d: invalid EXTINF fraction %q", lineNo, durText)
		}
		for i := len(frac); i < 3; i++ {
			f *= 10
		}
		if len(frac) > 3 {
			f /= pow10(len(frac) - 3)
		}
		ms += f
	}
	if neg {
		return 0, errs.New(errs.KindManifest, source, off, errs.Unknown, 0,
			"line %d: negative EXTINF duration", lineNo)
	}
	if ms <= 0 {
		return 0, errs.New(errs.KindManifest, source, off, errs.Unknown, 0,
			"line %d: EXTINF duration must be positive", lineNo)
	}
	return ms, nil
}

func parseByteRange(value, source string, off int64, lineNo int) (*Range, error) {
	parts := strings.Split(value, "@")
	if len(parts) != 2 {
		return nil, errs.New(errs.KindManifest, source, off, errs.Unknown, 0,
			"line %d: invalid EXT-X-BYTERANGE %q", lineNo, value)
	}
	length, err1 := strconv.ParseInt(parts[0], 10, 64)
	start, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || length <= 0 || start < 0 {
		return nil, errs.New(errs.KindManifest, source, off, errs.Unknown, 0,
			"line %d: invalid EXT-X-BYTERANGE %q", lineNo, value)
	}
	return &Range{Start: start, End: start + length}, nil
}

func pow10(n int) int64 {
	v := int64(1)
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}
