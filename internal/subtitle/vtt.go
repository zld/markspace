package subtitle

import (
	"io"
	"strings"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
	"mediamerge/internal/track"
)

// ParseVTT reads one UTF-8 WebVTT stream, skipping NOTE/STYLE/REGION blocks
// and accepting optional cue identifier lines.
func ParseVTT(source, version string, r io.Reader) (*track.Track, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fault.New(fault.StageIO, fault.KindIO, source, err.Error())
	}
	blocks, err := scanBlocks(source, byteReader(stripBOM(data)))
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 || !strings.HasPrefix(strings.TrimSpace(blocks[0].lines[0]), "WEBVTT") {
		return nil, fault.AtLine(fault.StageParse, fault.KindEncoding, source, 1,
			"WebVTT file must start with a WEBVTT header")
	}
	tr := &track.Track{ID: source, Format: "vtt", Version: version, Source: source}
	seq := 0
	var prevEnd clock.Ticks
	for bi := 1; bi < len(blocks); bi++ {
		blk := blocks[bi]
		head := strings.TrimSpace(blk.lines[0])
		switch {
		case strings.HasPrefix(head, "NOTE"), strings.HasPrefix(head, "STYLE"),
			strings.HasPrefix(head, "REGION"):
			continue
		case strings.HasPrefix(head, "WEBVTT"):
			continue
		}
		tsLine := head
		idLine := ""
		off := 0
		if strings.Index(head, "-->") < 0 {
			if len(blk.lines) < 2 {
				return nil, fault.AtLine(fault.StageParse, fault.KindTruncated, source, blk.startLine,
					"truncated WebVTT cue: identifier without a timestamp line")
			}
			idLine = head
			tsLine = strings.TrimSpace(blk.lines[1])
			off = 1
		}
		start, end, perr := parseInterval(tsLine)
		if perr != nil {
			return nil, withLine(perr, source, blk.startLine+off)
		}
		if end < start {
			return nil, fault.AtLine(fault.StageParse, fault.KindReversed, source, blk.startLine+off,
				"reversed cue interval")
		}
		if seq > 0 && start < prevEnd {
			return nil, fault.AtLine(fault.StageParse, fault.KindReversed, source, blk.startLine+off,
				"cue intervals must be in ascending order")
		}
		text := blk.lines[off+1:]
		if len(text) == 0 || strings.TrimSpace(strings.Join(text, "")) == "" {
			return nil, fault.AtLine(fault.StageParse, fault.KindTruncated, source, blk.startLine,
				"truncated WebVTT cue: text missing"+label(idLine))
		}
		seq++
		cue, cerr := buildCue(source, version, seq, start, end, text)
		if cerr != nil {
			return nil, withLine(cerr, source, blk.startLine+off)
		}
		tr.Cues = append(tr.Cues, cue)
		prevEnd = end
	}
	return tr, nil
}

func label(id string) string {
	if id == "" {
		return ""
	}
	return " (cue id " + id + ")"
}
