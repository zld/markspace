package pipeline

import (
	"bytes"

	"mediacheck/internal/ebml"
	"mediacheck/internal/matroska"
)

// bytesReader keeps pipeline source bytes behind the standard io.Reader the
// bounded EBML reader expects.
func bytesReader(doc []byte) *bytes.Reader { return bytes.NewReader(doc) }

func errClass(err error) ebml.ErrorClass {
	if pe, ok := err.(*matroska.ParseError); ok {
		return pe.Class
	}
	if ee, ok := err.(*ebml.Error); ok {
		return ee.Class
	}
	return ebml.ErrBadElement
}

func errOffset(err error) int64 {
	if pe, ok := err.(*matroska.ParseError); ok {
		return pe.Offset
	}
	if ee, ok := err.(*ebml.Error); ok {
		return ee.Offset
	}
	return -1
}
