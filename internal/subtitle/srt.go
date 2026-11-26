package subtitle

import (
	"io"
	"strings"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
	"mediamerge/internal/track"
)

type reader = io.Reader

func readAll(source string, r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fault.New(fault.StageIO, fault.KindIO, source, err.Error())
	}
	return data, nil
}

// ParseSRT reads one UTF-8 SRT stream. Every error carries the source name and
// the 1-based line where the problem was detected.
func ParseSRT(source, version string, r io.Reader) (*track.Track, error) {
	tr, _, err := ParseSRTWithOptions(source, version, r, Options{Rate: clock.Rate25})
	return tr, err
}

// parseSRTBlock parses one blank-line separated SRT block.
func parseSRTBlock(source, version string, blk rawBlock, seq *int,
	prevEnd *clock.Ticks, opt Options) (track.Cue, error) {
	if len(blk.lines) < 2 {
		return track.Cue{}, fault.AtLine(fault.StageParse, fault.KindTruncated, source, blk.startLine,
			"truncated SRT block: index and timestamp line required")
	}
	idxLine := strings.TrimSpace(blk.lines[0])
	if mustInt(idxLine) < 0 {
		return track.Cue{}, fault.AtLine(fault.StageParse, fault.KindTimecode, source, blk.startLine,
			"SRT block must begin with a numeric index, got "+idxLine)
	}
	start, end, perr := parseIntervalOpt(blk.lines[1], opt)
	if perr != nil {
		return track.Cue{}, withLine(perr, source, blk.startLine+1)
	}
	if end < start {
		return track.Cue{}, fault.AtLine(fault.StageParse, fault.KindReversed, source, blk.startLine+1,
			"reversed cue interval")
	}
	if *seq > 0 && start < *prevEnd {
		return track.Cue{}, fault.AtLine(fault.StageParse, fault.KindReversed, source, blk.startLine+1,
			"cue intervals must be in ascending order")
	}
	text := blk.lines[2:]
	if len(text) == 0 || strings.TrimSpace(strings.Join(text, "")) == "" {
		return track.Cue{}, fault.AtLine(fault.StageParse, fault.KindTruncated, source, blk.startLine,
			"truncated SRT block: cue text missing")
	}
	*seq++
	cue, cerr := buildCue(source, version, *seq, start, end, text)
	if cerr != nil {
		return track.Cue{}, withLine(cerr, source, blk.startLine+1)
	}
	*prevEnd = end
	return cue, nil
}

func byteReader(b []byte) io.Reader { return strings.NewReader(string(b)) }
