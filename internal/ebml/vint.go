// Package ebml implements a bounded, streaming reader for the subset of EBML
// used by Matroska and WebM. This file holds the variable-length integer
// primitives.
package ebml

import (
	"bufio"
	"errors"
	"io"

	"mediad/internal/merr"
)

// vintLen returns the encoded length from the leading byte's marker bit.
func vintLen(b byte) int {
	for n := 1; n <= 8; n++ {
		if b&(0x80>>(n-1)) != 0 {
			return n
		}
	}
	return 0
}

// peekID peeks at the next element id without consuming it. present is false
// at a clean EOF.
func (w *Walker) peekID() (ID, bool, error) {
	p, err := w.br.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return 0, false, err
	}
	if len(p) == 0 {
		return 0, false, nil
	}
	if p[0] == 0 {
		return 0, false, merr.New("ebml.peek", merr.CodeInvalidVINT, w.off, "zero first byte")
	}
	n := vintLen(p[0])
	if n == 0 || n > 4 {
		return 0, false, merr.New("ebml.peek", merr.CodeInvalidVINT, w.off, "invalid id length %d", n)
	}
	if len(p) < n {
		return 0, false, merr.New("ebml.peek", merr.CodeTruncatedElement, w.off,
			"input ended inside element id")
	}
	var id uint32
	for i := 0; i < n; i++ {
		id = id<<8 | uint32(p[i])
	}
	return ID(id), true, nil
}
