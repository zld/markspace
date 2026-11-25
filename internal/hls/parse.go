// Package hls parses offline HLS playlist fixtures into the same unified
// model.SegmentDescriptor stream as the DASH parser. An
// EXT-X-DISCONTINUITY marker advances the period identifier and every segment
// after it starts a fresh local timeline: segments from adjacent
// discontinuity periods are never stitched together by the parser.
package hls

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"mediasync/internal/model"
)

// Playlist is the parsed HLS playlist. DiscoCount counts the number of
// discontinuity markers observed; Errors holds non-fatal, located failures.
type Playlist struct {
	Segments   []model.SegmentDescriptor
	DiscoCount int
	Errors     []*model.MediaError
}

// Parse decodes the deterministic line-based m3u8 fixture format:
//
//	#EXTM3U
//	#EXT-X-TARGETDURATION:<seconds>
//	#EXTINF:<seconds>,<asset-kind>:<representation-id>:<index>[:keyframe]
//	<uri>
//	#EXT-X-DISCONTINUITY
//
// Every EXTINF must be followed by a URI line and a URI must be preceded by
// EXTINF; violations are reported at their 1-based line and skipped.
func Parse(data []byte, timescale uint32) (*Playlist, error) {
	if timescale == 0 {
		return nil, model.NewError(model.KindInvalidTimescale, "hls", 0,
			"playlist timescale must be non-zero")
	}
	p := &Playlist{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var pending *model.SegmentDescriptor
	lineNo := 0
	period := 0
	localStart := int64(0) // timescale units inside the current discontinuity period
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line == "#EXTM3U" || strings.HasPrefix(line, "#EXT-X-TARGETDURATION") {
			continue
		}
		switch {
		case line == "#EXT-X-DISCONTINUITY":
			if pending != nil {
				p.Errors = append(p.Errors, model.NewError(model.KindMalformedMP4, "hls",
					int64(lineNo), "discontinuity before URI for pending EXTINF at period %d", period))
				pending = nil
			}
			period++
			localStart = 0
			p.DiscoCount++
		case strings.HasPrefix(line, "#EXTINF:"):
			seg, err := parseExtinf(line, lineNo, period, localStart, timescale)
			if err != nil {
				p.Errors = append(p.Errors, err)
				pending = nil
				continue
			}
			pending = seg
		case strings.HasPrefix(line, "#"):
			// Unknown tags are ignored to stay tolerant of fixture extensions.
			continue
		default:
			// URI line: materialise the pending segment, reject orphan URIs.
			if pending == nil {
				p.Errors = append(p.Errors, model.NewError(model.KindMalformedMP4, "hls",
					int64(lineNo), "URI %q without preceding EXTINF", line))
				continue
			}
			pending.Payload = []byte(line)
			p.Segments = append(p.Segments, *pending)
			localStart += pending.DurationScale
			pending = nil
		}
	}
	if pending != nil {
		p.Errors = append(p.Errors, model.NewError(model.KindMalformedMP4, "hls",
			int64(lineNo), "dangling EXTINF without URI at end of playlist"))
	}
	return p, nil
}

func parseExtinf(line string, lineNo, period int, localStart int64, timescale uint32,
) (*model.SegmentDescriptor, *model.MediaError) {
	body := strings.TrimPrefix(line, "#EXTINF:")
	parts := strings.SplitN(body, ",", 2)
	if len(parts) != 2 {
		return nil, model.NewError(model.KindMalformedMP4, "hls", int64(lineNo),
			"EXTINF missing comma descriptor: %q", line)
	}
	secs, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || secs <= 0 {
		return nil, model.NewError(model.KindNegativeDuration, "hls", int64(lineNo),
			"EXTINF duration must be positive seconds, got %q", parts[0])
	}
	dur := int64(secs*float64(timescale) + 0.5)
	if dur <= 0 {
		return nil, model.NewError(model.KindNegativeDuration, "hls", int64(lineNo),
			"EXTINF duration rounds to zero at timescale %d", timescale)
	}
	meta := strings.Split(parts[1], ":")
	if len(meta) < 3 {
		return nil, model.NewError(model.KindMalformedMP4, "hls", int64(lineNo),
			"EXTINF descriptor needs kind:rep:index, got %q", parts[1])
	}
	kind, ok := parseKind(meta[0])
	if !ok {
		return nil, model.NewError(model.KindMalformedMP4, "hls", int64(lineNo),
			"unknown asset kind %q", meta[0])
	}
	idx, err := strconv.Atoi(meta[2])
	if err != nil {
		return nil, model.NewError(model.KindMalformedMP4, "hls", int64(lineNo),
			"invalid segment index %q: %v", meta[2], err)
	}
	keyframe := len(meta) > 3 && meta[3] == "keyframe"
	startNS, sErr := model.ScaleToNanoseconds(localStart, timescale)
	if sErr != nil {
		return nil, model.NewError(model.Classify(sErr), "hls", int64(lineNo), "%v", sErr)
	}
	durNS, dErr := model.ScaleDuration(dur, timescale)
	if dErr != nil {
		return nil, model.NewError(model.Classify(dErr), "hls", int64(lineNo), "%v", dErr)
	}
	return &model.SegmentDescriptor{
		Source: "hls", Representation: meta[1], PeriodID: period, Index: idx,
		Kind: kind, Keyframe: keyframe, StartNS: startNS, DurationNS: durNS,
		Timescale: timescale, StartInScale: localStart, DurationScale: dur,
	}, nil
}

func parseKind(s string) (model.AssetKind, bool) {
	switch s {
	case "dialogue":
		return model.KindDialogue, true
	case "subtitle":
		return model.KindSubtitle, true
	case "sfx":
		return model.KindSFX, true
	case "video":
		return model.KindVideo, true
	default:
		return 0, false
	}
}
