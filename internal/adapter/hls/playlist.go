// Package hls parses the local HLS media-playlist subset used by the QC
// pipeline: plain segment URIs, EXTINF durations, discontinuity markers and
// media sequence. Master playlists and tags outside the subset are rejected
// rather than silently ignored.
package hls

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mediaqc/internal/adapter"
	"mediaqc/internal/model"
)

// Segment is one parsed media segment reference.
type Segment struct {
	URI           string
	Sequence      uint64
	DurationTicks uint64
	Discontinuity bool
	Missing       bool // declared via a local "missing:" marker in fixtures
	Line          int
}

// Playlist is the parsed result plus structured diagnostics.
type Playlist struct {
	TargetDuration int
	MediaSequence  uint64
	Segments       []Segment
	Discontinuities []int // segment indexes starting a new discontinuity region
}

// Parser implements adapter.Parser for the HLS subset.
type Parser struct {
	limit adapter.Limits
}

// NewParser builds a parser with explicit limits.
func NewParser(limit adapter.Limits) *Parser { return &Parser{limit: limit} }

// Descriptor implements adapter.Parser.
func (p *Parser) Descriptor() adapter.Capability {
	return adapter.Capability{
		Kind: Kind(), Name: "m3u8-local", Version: "1.0.0",
		Features: []string{"discontinuity", "missing-marker"},
		Limit:    p.limit,
	}
}

// Supports implements adapter.Parser.
func (p *Parser) Supports(f string) bool { return p.Descriptor().Supports(f) }

// Kind returns the adapter kind constant.
func Kind() adapter.Kind { return adapter.KindHLS }

func init() { adapter.MustRegister(NewParser(adapter.DefaultLimits())) }

// Parse reads a media playlist. sizeHint bounds total input bytes.
func (p *Parser) Parse(materialID string, r io.Reader, sizeHint int64) (*Playlist, error) {
	if sizeHint > p.limit.MaxBytes {
		return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, "",
			fmt.Sprintf("playlist %d bytes exceeds limit %d", sizeHint, p.limit.MaxBytes))
	}
	sc := bufio.NewScanner(io.LimitReader(r, p.limit.MaxBytes+1))
	sc.Buffer(make([]byte, 0, 64*1024), int(p.limit.MaxBytes)+1)

	out := &Playlist{TargetDuration: -1}
	var (
		headerOK   bool
		nextDisco  bool
		pendingDur = -1.0
		seq        uint64
		seenSeq    = map[uint64]bool{}
		lines      int
	)
	for sc.Scan() {
		lines++
		if lines > p.limit.MaxSegments*4 {
			return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, "",
				"playlist line count exceeds nesting/line limit")
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !headerOK {
			if line != "#EXTM3U" {
				return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, "",
					"playlist must start with #EXTM3U")
			}
			headerOK = true
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			v, err := strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
			if err != nil || v <= 0 {
				return nil, model.Wrap(err, model.CodeConfig, model.StageParse, materialID, "",
					"invalid target duration")
			}
			out.TargetDuration = v
		case line == "#EXT-X-DISCONTINUITY":
			nextDisco = true
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			v, err := strconv.ParseUint(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
			if err != nil {
				return nil, model.Wrap(err, model.CodeConfig, model.StageParse, materialID, "",
					"invalid media sequence")
			}
			out.MediaSequence, seq = v, v
		case strings.HasPrefix(line, "#EXTINF:"):
			d, err := parseExtinf(strings.TrimPrefix(line, "#EXTINF:"))
			if err != nil {
				return nil, model.Wrap(err, model.CodeConfig, model.StageParse, materialID, "",
					"invalid EXTINF")
			}
			pendingDur = d
		case strings.HasPrefix(line, "#EXT-X-"):
			// Tags outside the supported subset are rejected, not ignored.
			return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, "",
				"unsupported HLS tag: "+line)
		case strings.HasPrefix(line, "#"):
			// Unknown comment lines are tolerated per RFC 8216.
			continue
		default:
			if pendingDur < 0 {
				return nil, model.NewError(model.CodeMissingReference, model.StageParse,
					materialID, "", "segment URI without preceding EXTINF at line "+strconv.Itoa(lines))
			}
			if len(out.Segments) >= p.limit.MaxSegments {
				return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, "",
					"segment count exceeds limit")
			}
			missing := strings.HasPrefix(line, "missing:")
			uri := strings.TrimPrefix(line, "missing:")
			if !missing && strings.TrimSpace(uri) == "" {
				return nil, model.NewError(model.CodeMissingReference, model.StageParse,
					materialID, "", "empty segment reference")
			}
			if seenSeq[seq] {
				return nil, model.NewError(model.CodeDuplicateSeq, model.StageParse,
					materialID, "", fmt.Sprintf("duplicate segment sequence %d", seq))
			}
			seenSeq[seq] = true
			seg := Segment{
				URI: uri, Sequence: seq,
				DurationTicks: uint64(pendingDur * 1000), // milliseconds
				Discontinuity: nextDisco, Missing: missing, Line: lines,
			}
			if nextDisco {
				out.Discontinuities = append(out.Discontinuities, len(out.Segments))
			}
			out.Segments = append(out.Segments, seg)
			seq++
			pendingDur, nextDisco = -1.0, false
		}
	}
	if err := sc.Err(); err != nil {
		return nil, model.Wrap(err, model.CodeTruncatedFragment, model.StageParse, materialID, "",
			"playlist scanner error")
	}
	if !headerOK {
		return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, "", "empty playlist")
	}
	if pendingDur >= 0 {
		return nil, model.NewError(model.CodeMissingReference, model.StageParse, materialID, "",
			"dangling EXTINF without segment URI")
	}
	return out, nil
}

// ParseBytes is a convenience wrapper over Parse.
func (p *Parser) ParseBytes(materialID string, b []byte) (*Playlist, error) {
	return p.Parse(materialID, bytes.NewReader(b), int64(len(b)))
}

func parseExtinf(v string) (float64, error) {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("extinf %q: %w", v, err)
	}
	return d, nil
}
