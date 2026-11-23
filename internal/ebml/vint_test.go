package ebml_test

import (
	"errors"
	"io"
	"testing"

	"mediad/internal/ebml"
	"mediad/internal/merr"
	"mediad/internal/testkit"
)

func TestLongSizeEncoding(t *testing.T) {
	// A 9-byte size VINT is invalid: first byte 0x00 has no marker.
	buf := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x00, 1, 2, 3}
	w := ebml.NewWalker(bytesReader(buf), ebml.ReaderOptions{})
	_, err := w.Next()
	if code := merr.CodeOf(err); code != merr.CodeInvalidVINT {
		t.Fatalf("code = %s want invalid_vint (%v)", code, err)
	}
	// A zero first id byte is also invalid.
	w = ebml.NewWalker(bytesReader([]byte{0, 0, 0, 0}), ebml.ReaderOptions{})
	_, err = w.Next()
	if code := merr.CodeOf(err); code != merr.CodeInvalidVINT {
		t.Fatalf("code = %s want invalid_vint (%v)", code, err)
	}
}

func TestChildOverrunsParent(t *testing.T) {
	// Info declares 4 bytes but its child TimecodeScale declares 100.
	info := testkit.IDBytes(ebml.IDInfo)
	body := append(testkit.IDBytes(ebml.IDTimecodeScale), testkit.SizeBytes(100)...)
	buf := append([]byte{}, info...)
	buf = append(buf, testkit.SizeBytes(uint64(len(body)+50))...)
	buf = append(buf, body...)
	buf = append(buf, make([]byte, 60)...)
	w := ebml.NewWalker(bytesReader(buf), ebml.ReaderOptions{})
	var err error
	for {
		if _, err = w.Next(); err != nil {
			break
		}
	}
	if code := merr.CodeOf(err); code != merr.CodeTruncatedElement {
		t.Fatalf("code = %s want truncated_element (%v)", code, err)
	}
}

func TestEmptyInputIsCleanEOF(t *testing.T) {
	w := ebml.NewWalker(bytesReader(nil), ebml.ReaderOptions{})
	if _, err := w.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("empty input: err = %v, want io.EOF", err)
	}
}
