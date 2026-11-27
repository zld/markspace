// Package subtitle parses SRT and WebVTT subtitle files into a normalised
// cue list on the shared nanosecond scale. Parsing never gives up at the first
// bad cue: it records the byte offset of every problem and keeps scanning, so
// the recoverable remainder of a truncated archive subtitle is still usable.
package subtitle

import (
	"strings"

	"archivecheck/internal/fault"
	"archivecheck/internal/timebase"
)

// Format identifies a subtitle container format.
type Format string

const (
	FormatSRT Format = "srt"
	FormatVTT Format = "vtt"
)

// Cue is one normalised subtitle cue.
type Cue struct {
	Index    int            `json:"index"`
	ID       string         `json:"id,omitempty"`
	Start    timebase.Nanos `json:"start"`
	End      timebase.Nanos `json:"end"`
	Lines    []string       `json:"lines"`
	Settings string         `json:"settings,omitempty"`
	Offset   int64          `json:"offset"`
}

// Text renders the cue body as newline-joined lines.
func (c Cue) Text() string { return strings.Join(c.Lines, "\n") }

// Problem is a recoverable parse failure located at a byte offset.
type Problem struct {
	Offset int64
	End    int64
	Err    error
}

// Track is the result of parsing one subtitle source. Problems are ordering
// independent byte spans; a cue whose span contains a problem was dropped.
type Track struct {
	Source   string    `json:"source"`
	Format   Format    `json:"format"`
	Cues     []Cue     `json:"cues"`
	Problems []Problem `json:"problems,omitempty"`
	Bytes    int64     `json:"bytes"`
}

// Bad reports whether any problem was found.
func (t *Track) Bad() bool { return len(t.Problems) > 0 }

// Parse detects the format from the leading bytes and parses accordingly.
// name is recorded on the track for provenance.
func Parse(name string, data []byte) *Track {
	t := &Track{Source: name, Bytes: int64(len(data))}
	if len(data) >= 3 && string(data[0:3]) == "\xef\xbb\xbf" {
		data = data[3:]
	}
	trimmed := strings.TrimLeft(string(data), " \t\r\n")
	switch {
	case strings.HasPrefix(trimmed, "WEBVTT"):
		t.Format = FormatVTT
		t.Cues, t.Problems = parseVTT(name, data)
	case strings.Contains(trimmed, "-->") || len(trimmed) == 0:
		t.Format = FormatSRT
		t.Cues, t.Problems = parseSRT(name, data)
	default:
		t.Format = FormatSRT
		t.Cues, t.Problems = parseSRT(name, data)
	}
	return t
}

// Err joins every problem into one error chain, or returns nil.
func (t *Track) Err() error {
	errs := make([]error, 0, len(t.Problems))
	for _, p := range t.Problems {
		errs = append(errs, p.Err)
	}
	return fault.Join(errs...)
}

// safeName strips any directory components so errors only ever cite the leaf.
func safeName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		return name[i+1:]
	}
	return name
}
