package subtitle

import (
	"bytes"
	"sort"
	"strings"

	"archivecheck/internal/clock"
	"archivecheck/internal/diagnostic"
)

// Cue is one parsed subtitle in the common nanosecond basis.
type Cue struct {
	Index        int               `json:"index"`
	ID           string            `json:"id,omitempty"`
	Start        clock.Nanoseconds `json:"start_ns"`
	End          clock.Nanoseconds `json:"end_ns"`
	Text         string            `json:"text"`
	StartOffset  int64             `json:"start_offset"`
	TimingOffset int64             `json:"timing_offset"`
}

// Track contains ordered, stably sorted cues and their unified timeline.
type Track struct {
	Format   Format         `json:"format"`
	Cues     []Cue          `json:"cues"`
	Timeline clock.Timeline `json:"timeline"`
}

func parseSubtitle(data []byte, sample int, format Format, limits Limits) (*Track, error) {
	limits = limits.withDefaults()
	if len(data) > limits.MaxInputBytes {
		return nil, diagnostic.New(diagnostic.CodeInputTooLarge, diagnostic.PhaseSubtitle, sample, 0,
			"subtitle input size %d exceeds limit %d", len(data), limits.MaxInputBytes)
	}
	if format == FormatVTT && !bytes.HasPrefix(bytes.TrimLeft(data, "\xef\xbb\xbf"), []byte("WEBVTT")) {
		return nil, diagnostic.New(diagnostic.CodeInvalidSubtitle, diagnostic.PhaseSubtitle, sample, 0,
			"WebVTT file must begin with WEBVTT signature")
	}

	sc := lineScanner{data: data, sample: sample, limits: limits}
	track := &Track{Format: format}
	var block []sourceLine

	flush := func(lines []sourceLine) error {
		if len(lines) == 0 {
			return nil
		}
		if len(track.Cues) >= limits.MaxCues {
			return diagnostic.New(diagnostic.CodeTooManyItems, diagnostic.PhaseSubtitle, sample, lines[0].start,
				"cue count exceeds limit %d", limits.MaxCues)
		}
		cue, err := parseBlock(lines, sample, format, len(track.Cues))
		if err != nil {
			return err
		}
		if cue == nil {
			return nil
		}
		track.Cues = append(track.Cues, *cue)
		return nil
	}

	for {
		line, err := sc.next()
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(line.text) == "" {
			if err := flush(block); err != nil {
				return nil, err
			}
			block = nil
		} else {
			block = append(block, line)
		}
		if line.eof {
			if err := flush(block); err != nil {
				return nil, err
			}
			break
		}
	}

	sort.SliceStable(track.Cues, func(i, j int) bool {
		return track.Cues[i].Start < track.Cues[j].Start
	})
	track.Timeline = clock.Timeline{}
	for _, cue := range track.Cues {
		if err := track.Timeline.Add(clock.Interval{
			Start: cue.Start, End: cue.End, Order: cue.Index,
			Sample: sample, Offset: cue.TimingOffset, Label: cue.Text,
		}); err != nil {
			return nil, err
		}
	}
	if err := track.Timeline.Normalize(); err != nil {
		return nil, err
	}
	return track, nil
}

func parseBlock(lines []sourceLine, sample int, format Format, order int) (*Cue, error) {
	timingIdx := -1
	for i, line := range lines {
		if strings.Contains(line.text, "-->") {
			timingIdx = i
			break
		}
	}
	if timingIdx < 0 {
		first := strings.TrimSpace(lines[0].text)
		if format == FormatVTT && (strings.HasPrefix(first, "WEBVTT") || strings.HasPrefix(first, "NOTE")) {
			return nil, nil
		}
		last := lines[len(lines)-1]
		code := diagnostic.CodeInvalidSubtitle
		if last.eof {
			code = diagnostic.CodeTruncated
		}
		return nil, diagnostic.New(code, diagnostic.PhaseSubtitle, sample, lines[0].start,
			"subtitle block has no cue timing line")
	}

	timing := lines[timingIdx]
	arrow := strings.Index(timing.text, "-->")
	left := firstToken(timing.text[:arrow])
	right := strings.Fields(timing.text[arrow+3:])
	if left == "" || len(right) == 0 {
		code := diagnostic.CodeInvalidTimestamp
		if timing.eof {
			code = diagnostic.CodeTruncated
		}
		return nil, diagnostic.New(code, diagnostic.PhaseSubtitle, sample, timing.start,
			"incomplete timing line %q", timing.text)
	}
	endToken := right[0]

	start, err := ParseTimestamp(left)
	if err != nil {
		off := timing.start + int64(strings.Index(timing.text, left))
		return nil, timestampFailureAt(err, timing.eof, sample, off, "invalid start timestamp %q", left)
	}
	endOffsetInLine := strings.Index(timing.text, endToken)
	end, err := ParseTimestamp(endToken)
	if err != nil {
		off := timing.start + int64(endOffsetInLine)
		return nil, timestampFailureAt(err, timing.eof, sample, off, "invalid end timestamp %q", endToken)
	}
	if end < start {
		return nil, diagnostic.New(diagnostic.CodeInvalidTimestamp, diagnostic.PhaseSubtitle, sample,
			timing.start, "end %d precedes start %d", end, start)
	}

	var id string
	if timingIdx > 0 {
		id = strings.TrimSpace(lines[0].text)
	}
	textLines := make([]string, 0, len(lines)-timingIdx-1)
	for _, line := range lines[timingIdx+1:] {
		textLines = append(textLines, line.text)
	}

	return &Cue{
		Index: order, ID: id, Start: start, End: end,
		Text: strings.Join(textLines, "\n"), StartOffset: timing.start, TimingOffset: timing.start,
	}, nil
}

func firstToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func timestampFailureAt(cause error, eof bool, sample int, offset int64, format string, args ...any) error {
	code := diagnostic.CodeInvalidTimestamp
	if eof {
		code = diagnostic.CodeTruncated
	}
	return diagnostic.Wrap(code, diagnostic.PhaseSubtitle, sample, offset, cause, format, args...)
}
