// Package cue defines the format-independent subtitle model shared by every
// parser, timebase mapping stage and serializer.
package cue

import (
	"fmt"
	"time"
)

// Format identifies one of the supported subtitle container formats.
type Format string

const (
	FormatSRT Format = "srt"
	FormatVTT Format = "vtt"
	FormatASS Format = "ass"
)

// ParseFormat resolves a user supplied format name.
func ParseFormat(s string) (Format, error) {
	switch s {
	case "srt", "SRT":
		return FormatSRT, nil
	case "vtt", "VTT", "webvtt", "WebVTT":
		return FormatVTT, nil
	case "ass", "ASS":
		return FormatASS, nil
	default:
		return "", fmt.Errorf("unknown format %q (want srt, vtt or ass)", s)
	}
}

// Cue is a single subtitle entry. Durations are absolute offsets from the start
// of the media timeline. SourceLine points at the line (1-based) where the cue
// began in the source file and is used in error messages.
type Cue struct {
	ID         string
	Text       string
	Start      time.Duration
	End        time.Duration
	SourceLine int
}

// Document is the parsed content of one subtitle file.
type Document struct {
	Format Format
	Cues   []Cue
}

// Duration returns the cue length.
func (c Cue) Duration() time.Duration {
	return c.End - c.Start
}
