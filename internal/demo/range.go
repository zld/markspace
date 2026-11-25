// Package demo wires fixtures, pipeline recovery and terminal acceptance output.
package demo

import (
	"strconv"
	"strings"

	"mediacore/internal/clock"
	"mediacore/internal/index"
)

const nanosPerSecond = int64(1_000_000_000)

// ParseRange parses "HH:MM:SS,HH:MM:SS" (MM:SS and seconds also accepted).
func ParseRange(s string) (index.Range, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return index.Range{}, clock.New(clock.CategoryInvalidArgument, "--range", 0, 0,
			"range must be start,end in HH:MM:SS,HH:MM:SS form")
	}
	start, err := ParseTime(parts[0])
	if err != nil {
		return index.Range{}, err
	}
	end, err := ParseTime(parts[1])
	if err != nil {
		return index.Range{}, err
	}
	if end <= start {
		return index.Range{}, clock.New(clock.CategoryInvalidArgument, "--range", 0, 0, "range end must be after start")
	}
	return index.Range{Start: clock.EventTime(start), End: clock.EventTime(end)}, nil
}

// ParseTime parses colon-separated seconds/minutes/hours into nanoseconds.
func ParseTime(s string) (int64, error) {
	fields := strings.Split(strings.TrimSpace(s), ":")
	if len(fields) == 0 || len(fields) > 3 {
		return 0, clock.New(clock.CategoryInvalidArgument, "--range", 0, 0, "time must have at most HH:MM:SS")
	}
	var seconds int64
	for _, f := range fields {
		v, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || v < 0 {
			return 0, clock.New(clock.CategoryInvalidArgument, "--range", 0, 0, "invalid time "+s)
		}
		seconds = seconds*60 + int64(v)
	}
	return seconds * nanosPerSecond, nil
}
