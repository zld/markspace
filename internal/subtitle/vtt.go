package subtitle

import (
	"errors"
	"strings"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
	"mediamerge/internal/track"
)

var errSkipBlock = errors.New("vtt block skipped (NOTE/STYLE/REGION/header)")

// ParseVTT reads one UTF-8 WebVTT stream, skipping NOTE/STYLE/REGION blocks.
func ParseVTT(source, version string, r reader) (*track.Track, error) {
	tr, _, err := ParseVTTWithOptions(source, version, r, Options{Rate: clock.Rate25})
	return tr, err
}

// parseVTTBlock parses one WebVTT block, returning errSkipBlock for non-cue
// blocks such as NOTE/STYLE/REGION.
func parseVTTBlock(source, version string, blk rawBlock, seq *int,
	prevEnd *clock.Ticks, opt Options) (track.Cue, error) {
	head := strings.TrimSpace(blk.lines[0])
	switch {
	case strings.HasPrefix(head, "NOTE"), strings.HasPrefix(head, "STYLE"),
		strings.HasPrefix(head, "REGION"), strings.HasPrefix(head, "WEBVTT"):
		return track.Cue{}, errSkipBlock
	}
	tsLine := head
	idLine := ""
	off := 0
	if strings.Index(head, "-->") < 0 {
		if len(blk.lines) < 2 {
			return track.Cue{}, fault.AtLine(fault.StageParse, fault.KindTruncated, source, blk.startLine,
				"truncated WebVTT cue: identifier without a timestamp line")
		}
		idLine = head
		tsLine = strings.TrimSpace(blk.lines[1])
		off = 1
	}
	start, end, perr := parseIntervalOpt(tsLine, opt)
	if perr != nil {
		return track.Cue{}, withLine(perr, source, blk.startLine+off)
	}
	if end < start {
		return track.Cue{}, fault.AtLine(fault.StageParse, fault.KindReversed, source, blk.startLine+off,
			"reversed cue interval")
	}
	if *seq > 0 && start < *prevEnd {
		return track.Cue{}, fault.AtLine(fault.StageParse, fault.KindReversed, source, blk.startLine+off,
			"cue intervals must be in ascending order")
	}
	text := blk.lines[off+1:]
	if len(text) == 0 || strings.TrimSpace(strings.Join(text, "")) == "" {
		return track.Cue{}, fault.AtLine(fault.StageParse, fault.KindTruncated, source, blk.startLine,
			"truncated WebVTT cue: text missing"+label(idLine))
	}
	*seq++
	cue, cerr := buildCue(source, version, *seq, start, end, text)
	if cerr != nil {
		return track.Cue{}, withLine(cerr, source, blk.startLine+off)
	}
	*prevEnd = end
	return cue, nil
}

func label(id string) string {
	if id == "" {
		return ""
	}
	return " (cue id " + id + ")"
}
