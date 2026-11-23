// Package ebml implements a bounded, streaming reader for the subset of EBML
// used by Matroska and WebM. This file reads bounded leaf payloads.
package ebml

import (
	"errors"
	"fmt"
	"io"

	"mediad/internal/merr"
)

// remaining reports how many trailing bytes of b were not filled; readFull
// stores the shortfall in a sentinel error.
type shortRead struct{ n int }

func (s *shortRead) Error() string { return fmt.Sprintf("short read: %d", s.n) }

func remaining(err error) int {
	var sr *shortRead
	if errors.As(err, &sr) {
		return sr.n
	}
	return 0
}

func (w *Walker) readFull(buf []byte) error {
	n, err := io.ReadFull(w.br, buf)
	w.off += int64(n)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return &shortRead{n: len(buf) - n}
		}
		return err
	}
	return nil
}

// readID reads an EBML id (1-4 byte VINT).
func (w *Walker) readID() (ID, error) {
	b, err := w.br.ReadByte()
	if err != nil {
		return 0, err
	}
	w.off++
	if b == 0 {
		return 0, merr.New("ebml.id", merr.CodeInvalidVINT, w.off-1, "zero first byte")
	}
	n := vintLen(b)
	if n > 4 {
		return 0, merr.New("ebml.id", merr.CodeInvalidVINT, w.off-1,
			"id length %d exceeds 4 bytes", n)
	}
	id := uint32(b)
	for i := 1; i < n; i++ {
		c, err := w.br.ReadByte()
		if err != nil {
			// A partially consumed id is truncation, not a clean EOF.
			return 0, io.ErrUnexpectedEOF
		}
		w.off++
		id = id<<8 | uint32(c)
	}
	return ID(id), nil
}

// readSize reads an EBML size VINT (1-8 bytes).
func (w *Walker) readSize() (size uint64, unknown bool, err error) {
	var first [1]byte
	if _, err = io.ReadFull(w.br, first[:]); err != nil {
		return 0, false, err
	}
	w.off++
	b := first[0]
	if b == 0 {
		return 0, false, merr.New("ebml.size", merr.CodeInvalidVINT, w.off-1, "zero first byte")
	}
	n := vintLen(b)
	if n > 8 {
		return 0, false, merr.New("ebml.size", merr.CodeInvalidVINT, w.off-1,
			"size length %d exceeds 8 bytes", n)
	}
	val := uint64(b & byte(0xFF>>n))
	for i := 1; i < n; i++ {
		c, err := w.br.ReadByte()
		if err != nil {
			return 0, false, io.ErrUnexpectedEOF
		}
		w.off++
		val = val<<8 | uint64(c)
	}
	// All value bits set is the reserved "unknown size" marker.
	if n < 8 {
		allOnes := (uint64(1) << (7 * n)) - 1
		if val == allOnes {
			return 0, true, nil
		}
	} else if val == (uint64(1)<<56)-1 {
		return 0, true, nil
	}
	return val, false, nil
}
