// Package mp4x cross-checks a rebuilt Matroska random-access index against an
// MP4 index produced with github.com/abema/go-mp4. This file holds the
// low-level box writer used by Build.
package mp4x

import (
	"bytes"
	"fmt"
	"io"

	"github.com/abema/go-mp4"
)

// writeBox writes one leaf box: header, payload, then the patched size.
func writeBox(w *mp4.Writer, typ mp4.BoxType, box mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

// boxSize reports the serialized size of a single box.
func boxSize(box mp4.IImmutableBox) (uint64, error) {
	var buf bytes.Buffer
	n, err := mp4.Marshal(&buf, box, mp4.Context{})
	if err != nil {
		return 0, err
	}
	return n + 8, nil // + 8-byte box header
}

// writeSeeker supports the seek-back patching go-mp4 performs when it closes
// a box.
type writeSeeker struct {
	buf *bytes.Buffer
	off int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	if w.off > int64(w.buf.Len()) {
		return 0, fmt.Errorf("mp4x: seek past end")
	}
	if w.off < int64(w.buf.Len()) {
		n := copy(w.buf.Bytes()[w.off:], p)
		w.off += int64(n)
		return n, nil
	}
	n, err := w.buf.Write(p)
	w.off += int64(n)
	return n, err
}

func (w *writeSeeker) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		w.off = off
	case io.SeekCurrent:
		w.off += off
	case io.SeekEnd:
		w.off = int64(w.buf.Len()) + off
	}
	return w.off, nil
}
