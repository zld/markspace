// Package format parses and serializes SRT and WebVTT subtitle files.
package format

import (
	"fmt"

	"subx/internal/cue"
)

// Format names a subtitle container format.
type Format string

const (
	SRT Format = "srt"
	VTT Format = "vtt"
)

// ParseError reports a malformed subtitle entry with file and line number.
type ParseError struct {
	File string
	Line int
	Msg  string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Msg)
}

// Parse dispatches to the parser for the named format.
func Parse(f Format, text, file string) ([]cue.Cue, error) {
	switch f {
	case SRT:
		return ParseSRT(text, file)
	case VTT:
		return ParseVTT(text, file)
	}
	return nil, fmt.Errorf("unknown input format %q", f)
}

// Serialize dispatches to the serializer for the named format.
func Serialize(f Format, cues []cue.Cue) ([]byte, error) {
	switch f {
	case SRT:
		return SerializeSRT(cues), nil
	case VTT:
		return SerializeVTT(cues), nil
	}
	return nil, fmt.Errorf("unknown output format %q", f)
}
