package subtitle

import (
	"io"
	"strings"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
	"mediamerge/internal/track"
)

// ParseSRT reads one UTF-8 SRT stream. Every error carries the source name and
// the 1-based line where the problem was detected.
func ParseSRT(source, version string, r io.Reader) (*track.Track, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fault.New(fault.StageIO, fault.KindIO, source, err.Error())
	}
	blocks, err := scanBlocks(source, byteReader(stripBOM(data)))
	if err != nil {
		return nil, err
	}
	tr := &track.Track{ID: source, Format: "srt", Version: version, Source: source}
	seq := 0
	var prevEnd clock.Ticks
	for _, blk := range blocks {
		if len(blk.lines) < 2 {
			return nil, fault.AtLine(fault.StageParse, fault.KindTruncated, source, blk.startLine,
				"truncated SRT block: index and timestamp line required")
		}
		idxLine := strings.TrimSpace(blk.lines[0])
		if mustInt(idxLine) < 0 {
			return nil, fault.AtLine(fault.StageParse, fault.KindTimecode, source, blk.startLine,
				"SRT block must begin with a numeric index, got "+idxLine)
		}
		start, end, perr := parseInterval(blk.lines[1])
		if perr != nil {
			return nil, withLine(perr, source, blk.startLine+1)
		}
		if end < start {
			return nil, fault.AtLine(fault.StageParse, fault.KindReversed, source, blk.startLine+1,
				"reversed cue interval")
		}
		if seq > 0 && start < prevEnd {
			return nil, fault.AtLine(fault.StageParse, fault.KindReversed, source, blk.startLine+1,
				"cue intervals must be in ascending order")
		}
		text := blk.lines[2:]
		if len(text) == 0 || strings.TrimSpace(strings.Join(text, "")) == "" {
			return nil, fault.AtLine(fault.StageParse, fault.KindTruncated, source, blk.startLine,
				"truncated SRT block: cue text missing")
		}
		seq++
		cue, cerr := buildCue(source, version, seq, start, end, text)
		if cerr != nil {
			return nil, withLine(cerr, source, blk.startLine+1)
		}
		tr.Cues = append(tr.Cues, cue)
		prevEnd = end
	}
	return tr, nil
}

// parseInterval splits "start --> end" into two tick timestamps.
func parseInterval(line string) (clock.Ticks, clock.Ticks, error) {
	arrow := "-->"
	i := strings.Index(line, arrow)
	if i < 0 {
		return 0, 0, fault.New(fault.StageParse, fault.KindTimecode, "",
			"timestamp line must contain -->: "+strings.TrimSpace(line))
	}
	left := strings.TrimSpace(line[:i])
	right := strings.TrimSpace(line[i+len(arrow):])
	// VTT allows trailing cue settings after the end timestamp.
	if sp := strings.IndexAny(right, " \t"); sp >= 0 {
		right = right[:sp]
	}
	start, err := parseClock(left)
	if err != nil {
		return 0, 0, err
	}
	end, err := parseClock(right)
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

func withLine(err error, source string, line int) error {
	if fe := fault.As(err); fe != nil && fe.Line == 0 {
		fe.Source = source
		fe.Line = line
	}
	return err
}

func byteReader(b []byte) io.Reader { return strings.NewReader(string(b)) }
