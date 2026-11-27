package ingest

import (
	"encoding/binary"
	"io"
)

// Writer emits the record stream format. It is used by the sample generator and
// by tests to build fixtures without external files.
type Writer struct {
	w     io.Writer
	n     int64
	start int64
}

// NewWriter writes the stream header and returns a Writer positioned at the
// first record.
func NewWriter(w io.Writer, startUnixNano int64) (*Writer, error) {
	hdr := make([]byte, HeaderSize)
	copy(hdr[0:4], "RTRB")
	hdr[4] = 1
	binary.BigEndian.PutUint64(hdr[8:16], uint64(startUnixNano))
	if _, err := w.Write(hdr); err != nil {
		return nil, err
	}
	return &Writer{w: w, n: HeaderSize, start: startUnixNano}, nil
}

// Offset returns the absolute offset the next record will be written at.
func (w *Writer) Offset() int64 { return w.n }

// WriteRecord frames and writes one payload, returning its offset.
func (w *Writer) WriteRecord(kind Kind, payload []byte) (int64, error) {
	off := w.n
	hdr := make([]byte, RecordHeaderSize)
	hdr[0] = byte(kind)
	binary.BigEndian.PutUint32(hdr[2:6], uint32(len(payload)))
	if _, err := w.w.Write(hdr); err != nil {
		return 0, err
	}
	if _, err := w.w.Write(payload); err != nil {
		return 0, err
	}
	w.n += RecordHeaderSize + int64(len(payload))
	return off, nil
}

// Size returns the number of bytes written so far.
func (w *Writer) Size() int64 { return w.n }
