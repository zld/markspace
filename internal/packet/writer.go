package packet

import (
	"encoding/binary"
	"io"

	"mediaforge/internal/mediaerr"
)

// Writer emits a framed capture in the format ReadStream accepts.
type Writer struct {
	w   io.Writer
	buf []byte
}

// NewWriter starts a capture stream.
func NewWriter(w io.Writer) (*Writer, error) {
	if _, err := io.WriteString(w, Magic); err != nil {
		return nil, mediaerr.New(mediaerr.CodeIO, "write capture magic: %v", err)
	}
	return &Writer{w: w}, nil
}

// WriteRecord appends one RTP packet or RTCP datagram.
func (w *Writer) WriteRecord(kind RecordKind, body []byte) error {
	if len(body) == 0 || len(body) > MaxPacketSize {
		return mediaerr.New(mediaerr.CodeParam, "record size %d outside 1..%d", len(body), MaxPacketSize)
	}
	if len(w.buf) < 5+len(body) {
		w.buf = make([]byte, 5+len(body))
	}
	binary.BigEndian.PutUint32(w.buf[:4], uint32(len(body)))
	w.buf[4] = byte(kind)
	copy(w.buf[5:], body)
	_, err := w.w.Write(w.buf[:5+len(body)])
	if err != nil {
		return mediaerr.New(mediaerr.CodeIO, "write capture record: %v", err)
	}
	return nil
}
