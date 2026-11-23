// Package subtitle parses SRT and WebVTT into a unified tick timeline.
package subtitle

import (
	"fmt"
	"io"

	"mediqc/internal/clock"
	"mediqc/internal/fault"
)

// Cue is one subtitle interval on the unified 90kHz timeline.
type Cue struct {
	Index int    // 1-based sequence number from the source
	Start int64  // inclusive, in clock.Timescale ticks
	End   int64  // exclusive, in clock.Timescale ticks
	Text  string // payload lines joined by \n
}

// Duration returns the cue length in ticks.
func (c Cue) Duration() int64 { return c.End - c.Start }

// Timeline is an ordered cue list with its source format.
type Timeline struct {
	Format string // "srt" or "vtt"
	Cues   []Cue
}

// Parse dispatches on format ("srt" or "vtt").
func Parse(format string, r io.Reader) (*Timeline, error) {
	switch format {
	case "srt":
		return ParseSRT(r)
	case "vtt":
		return ParseVTT(r)
	}
	return nil, fault.New(fault.CodeBadTimecode, 0, -1,
		"unknown subtitle format "+format, "supported: srt, vtt")
}

// dayTicks is the number of ticks in 24 hours; timestamps at or beyond
// it are rejected as day-boundary crossings.
const dayTicks = 24 * 3600 * clock.Timescale

// checkRange validates a parsed cue interval.
func checkRange(line int, start, end int64) error {
	if start < 0 || end < 0 {
		return fault.New(fault.CodeNegativeDuration, line, -1,
			"negative timestamp", "timestamps must be >= 00:00:00")
	}
	if end <= start {
		return fault.New(fault.CodeNegativeDuration, line, -1,
			fmt.Sprintf("cue end <= start (%d <= %d ticks)", end, start),
			"give every cue a positive duration")
	}
	if start >= dayTicks || end > dayTicks {
		return fault.New(fault.CodeDayOverflow, line, -1,
			"timestamp crosses the 24h day boundary",
			"split the subtitle file at midnight")
	}
	return nil
}

// hmsToTicks converts h:m:s.millis to ticks with range checks.
func hmsToTicks(line int, hh, mm, ss, ms int, raw string) (int64, error) {
	if mm >= 60 || ss >= 60 || ms >= 1000 {
		return 0, fault.New(fault.CodeBadTimecode, line, -1,
			"out-of-range field in "+raw, "want MM<60 SS<60 millis<1000")
	}
	if hh >= 24 {
		return 0, fault.New(fault.CodeDayOverflow, line, -1,
			"hours >= 24 in "+raw, "split the subtitle file at midnight")
	}
	return ((int64(hh)*3600+int64(mm)*60+int64(ss))*1000 + int64(ms)) *
		(clock.Timescale / 1000), nil
}
