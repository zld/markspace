// Package codec parses variable-length Opus and AAC packets and reports the
// exact number of source samples each packet contributes. Truncated or
// illegal packets fail with a stable model.Error instead of an estimated
// duration so no stage downstream processes guessed timing.
package codec

import "github.com/example/mediad/internal/model"

// Frame is one parsed codec packet: its payload duration in source samples
// and the TOC-derived per-frame count.
type Frame struct {
	Samples    uint32
	FrameCount int
}

// Parser turns one wire payload into a Frame. Implementations must return a
// *model.Error on any malformed input.
type Parser interface {
	// Name reports the codec identifier ("opus" or "aac").
	Name() string
	// Rate reports the source sample rate the stream is clocked at.
	Rate() uint32
	// Parse validates payload and returns its sample duration.
	Parse(seq uint16, offset int64, payload []byte) (Frame, error)
}

// ensure model.Error is constructable from both subpackages via one helper.
func errAt(code model.Code, stage model.Stage, seq uint16, offset int64, detail string) error {
	return model.NewError(code, stage, seq, offset, detail)
}
