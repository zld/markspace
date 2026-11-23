package packet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	"mediaforge/internal/mediaerr"
)

// ReadStream decodes a framed capture. Framing failures are fatal; packet-level
// failures are returned as records with Err set so later good packets survive.
func ReadStream(r io.Reader, sizeLimit int64) (*Stream, error) {
	if sizeLimit <= 0 || sizeLimit > MaxInputSize {
		sizeLimit = MaxInputSize
	}
	lr := &io.LimitedReader{R: r, N: sizeLimit + 1}
	magic := make([]byte, len(Magic))
	n, err := io.ReadFull(lr, magic)
	if err != nil {
		return nil, mediaerr.New(mediaerr.CodeTruncated, "capture header: %v", err)
	}
	if !bytes.Equal(magic, []byte(Magic)) {
		return nil, mediaerr.New(mediaerr.CodeBadPacket, "capture magic mismatch")
	}
	s := &Stream{Bytes: int64(n)}
	var hdr [5]byte
	for {
		n, err := io.ReadFull(lr, hdr[:])
		s.Bytes += int64(n)
		if errors.Is(err, io.EOF) && n == 0 {
			break
		}
		if err != nil {
			return s, mediaerr.At(mediaerr.CodeTruncated, s.Bytes-int64(n), 0, false,
				"capture record header truncated")
		}
		length := int(binary.BigEndian.Uint32(hdr[:4]))
		kind := RecordKind(hdr[4])
		recOffset := s.Bytes
		if length <= 0 || length > MaxPacketSize {
			return s, mediaerr.At(mediaerr.CodeBadPacket, recOffset, 0, false,
				"record length %d outside 1..%d", length, MaxPacketSize)
		}
		if kind != KindRTP && kind != KindRTCP {
			return s, mediaerr.At(mediaerr.CodeBadPacket, recOffset, 0, false,
				"unknown record kind %d", kind)
		}
		body := make([]byte, length)
		n, err = io.ReadFull(lr, body)
		s.Bytes += int64(n)
		if err != nil {
			return s, mediaerr.At(mediaerr.CodeTruncated, recOffset, 0, false,
				"record declares %d bytes, only %d available", length, n)
		}
		rec := Record{Kind: kind, Offset: recOffset}
		if kind == KindRTP {
			p, perr := ParsePacket(body, recOffset)
			rec.Packet, rec.Err = p, perr
		} else {
			b, perr := ParseRTCP(body, recOffset)
			rec.RTCP, rec.Err = b, perr
		}
		s.Records = append(s.Records, rec)
	}
	if lr.N == 0 {
		return s, mediaerr.New(mediaerr.CodeBadPacket, "capture exceeds %d byte limit", sizeLimit)
	}
	return s, nil
}
