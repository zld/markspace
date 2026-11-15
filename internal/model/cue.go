// Package model defines the format-independent subtitle representation shared
// by every parser, serializer and verification stage of the pipeline.
package model

import (
	"fmt"
	"strings"
	"time"
)

// Format identifies one of the supported subtitle syntaxes.
type Format string

// Supported subtitle formats.
const (
	FormatSRT Format = "srt"
	FormatVTT Format = "vtt"
	FormatASS Format = "ass"
)

// ParseFormat resolves a user supplied format name.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "srt":
		return FormatSRT, nil
	case "vtt", "webvtt":
		return FormatVTT, nil
	case "ass", "ssa":
		return FormatASS, nil
	default:
		return "", fmt.Errorf("unknown subtitle format %q (want srt|vtt|ass)", s)
	}
}

// Cue is one unified subtitle entry. Timing is stored as nanosecond offsets
// from the start of the media so that all arithmetic stays integer based.
type Cue struct {
	// ID is the identifier carried by the source format (SRT index, VTT id or
	// generated ASS line id). Empty when the source provides none.
	ID string
	// Track groups cues that share an independent timeline.
	Track string
	// Order preserves the original position of the cue inside its source file.
	Order int
	// Start and End are nanosecond offsets relative to media start.
	Start time.Duration
	End   time.Duration
	// Text is the cue body with normalized newlines ("\n"); formatting tags
	// stay literal because the tool performs no markup conversion.
	Text string
	// Style is the ASS style name ("Default" when absent). Empty for formats
	// that do not carry styles.
	Style string
	// Layer is the ASS dialogue layer; zero for every other format.
	Layer int
}

// Document is the result of parsing one input file.
type Document struct {
	Format Format
	// Cues retain parse order; later stages sort copies as needed.
	Cues []Cue
	// Styles maps ASS style name to its raw definition line. Nil for the
	// non-ASS formats and used when re-emitting ASS output.
	Styles map[string]string
	// StyleLoss counts cues whose ASS style cannot be represented in a
	// non-ASS target format.
	StyleLoss int
}

// ParseError ties a syntax problem to the 1-based line of the source file.
type ParseError struct {
	Line int
	Msg  string
}

// Error implements error.
func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

// ParseErrors is a batch of per-entry errors, used in skip-bad mode.
type ParseErrors []*ParseError

// Error implements error.
func (errs ParseErrors) Error() string {
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}

// ParseMode selects how malformed entries are treated.
type ParseMode int

// Supported parse modes.
const (
	// ParseFail makes the first malformed entry fail the whole batch.
	ParseFail ParseMode = iota
	// ParseSkip records the error and drops the offending entry instead.
	ParseSkip
)
