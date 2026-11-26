package subtitle

import (
	"strings"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
	"mediamerge/internal/track"
)

// Options controls parser strictness and the frame rate used for timecode
// labels that carry drop-frame punctuation.
type Options struct {
	// Lenient collects malformed cues into Rejected instead of aborting.
	Lenient bool
	// Rate is the frame rate used to validate drop-frame labels.
	Rate clock.Rate
}

// RejectedCue pairs a malformed cue location with its stage fault.
type RejectedCue struct {
	Source string
	Line   int
	Kind   fault.Kind
	Detail string
}

// ParseSRTWithOptions parses SRT, returning rejected cues when Lenient is set.
func ParseSRTWithOptions(source, version string, r reader, opt Options) (*track.Track, []RejectedCue, error) {
	data, err := readAll(source, r)
	if err != nil {
		return nil, nil, err
	}
	blocks, err := scanBlocks(source, byteReader(stripBOM(data)))
	if err != nil {
		if opt.Lenient {
			return &track.Track{ID: source, Format: "srt", Version: version, Source: source}, nil, nil
		}
		return nil, nil, err
	}
	tr := &track.Track{ID: source, Format: "srt", Version: version, Source: source}
	var rejected []RejectedCue
	seq := 0
	var prevEnd clock.Ticks
	for _, blk := range blocks {
		cue, perr := parseSRTBlock(source, version, blk, &seq, &prevEnd, opt)
		if perr != nil {
			fe := fault.As(perr)
			if !opt.Lenient || fe == nil {
				return nil, nil, perr
			}
			rejected = append(rejected, RejectedCue{Source: source, Line: fe.Line,
				Kind: fe.Kind, Detail: fe.Detail})
			continue
		}
		tr.Cues = append(tr.Cues, cue)
	}
	return tr, rejected, nil
}

// ParseVTTWithOptions is the lenient-capable WebVTT entry point.
func ParseVTTWithOptions(source, version string, r reader, opt Options) (*track.Track, []RejectedCue, error) {
	data, err := readAll(source, r)
	if err != nil {
		return nil, nil, err
	}
	blocks, err := scanBlocks(source, byteReader(stripBOM(data)))
	if err != nil {
		if opt.Lenient {
			return &track.Track{ID: source, Format: "vtt", Version: version, Source: source}, nil, nil
		}
		return nil, nil, err
	}
	if len(blocks) == 0 || !strings.HasPrefix(strings.TrimSpace(blocks[0].lines[0]), "WEBVTT") {
		return nil, nil, fault.AtLine(fault.StageParse, fault.KindEncoding, source, 1,
			"WebVTT file must start with a WEBVTT header")
	}
	tr := &track.Track{ID: source, Format: "vtt", Version: version, Source: source}
	var rejected []RejectedCue
	seq := 0
	var prevEnd clock.Ticks
	for bi := 1; bi < len(blocks); bi++ {
		cue, perr := parseVTTBlock(source, version, blocks[bi], &seq, &prevEnd, opt)
		if perr == errSkipBlock {
			continue
		}
		if perr != nil {
			fe := fault.As(perr)
			if !opt.Lenient || fe == nil {
				return nil, nil, perr
			}
			rejected = append(rejected, RejectedCue{Source: source, Line: fe.Line,
				Kind: fe.Kind, Detail: fe.Detail})
			continue
		}
		tr.Cues = append(tr.Cues, cue)
	}
	return tr, rejected, nil
}

func withLine(err error, source string, line int) error {
	if fe := fault.As(err); fe != nil && fe.Line == 0 {
		fe.Source = source
		fe.Line = line
	}
	return err
}
