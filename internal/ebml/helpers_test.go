package ebml_test

import (
	"errors"
	"io"
	"testing"

	"mediad/internal/ebml"
)

func collect(t *testing.T, data []byte) ([]ebml.Event, error) {
	t.Helper()
	w := ebml.NewWalker(bytesReader(data), ebml.ReaderOptions{})
	var evs []ebml.Event
	for {
		ev, err := w.Next()
		if errors.Is(err, io.EOF) {
			return evs, nil
		}
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
}

type sliceReader struct {
	b []byte
	i int
}

func bytesReader(b []byte) *sliceReader { return &sliceReader{b: b} }

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
