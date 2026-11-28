// Package ingest reads bounded, checksummed frame records from a local byte
// stream. The on-wire record is fully self describing so fixtures can be
// generated without any external media file.
//
// Record layout (all integers big-endian):
//
//	header (40 bytes)
//	  [0:4]   magic 0x4D434631 ("MCF1")
//	  [4:8]   track id
//	  [8:12]  source id
//	  [12:16] sequence number
//	  [16:24] event time ticks (int64)
//	  [24:28] payload length
//	  [28]    frame type (1=key, 2=delta)
//	  [29:32] reserved
//	  [32:36] time base numerator
//	  [36:40] time base denominator
//	payload (payload length bytes)
//	crc32 IEEE over header+payload (4 bytes)
package ingest

import (
	"encoding/binary"
	"hash/crc32"
	"io"

	"medcheck/internal/model"
)

const (
	// Magic identifies a medcheck frame record.
	Magic uint32 = 0x4D434631
	// HeaderSize is the fixed record header size.
	HeaderSize = 40
	// crcSize is the trailing checksum width.
	crcSize = 4
)

// EncodeInput carries everything needed to serialize one frame record.
type EncodeInput struct {
	Track      uint32
	Source     uint32
	Seq        uint32
	EventTicks int64
	Base       model.Rational
	FrameType  model.FrameType
	Payload    []byte
	// Corrupt flips one payload byte after the CRC was computed so the reader
	// observes a genuine checksum failure.
	Corrupt bool
}

// EncodeRecord serializes one record, enforcing the payload size limit.
func EncodeRecord(in EncodeInput) ([]byte, error) {
	if len(in.Payload) > model.MaxFramePayload {
		return nil, model.LengthError(model.RawOffset{}, "frame payload exceeds limit")
	}
	buf := make([]byte, HeaderSize+len(in.Payload)+crcSize)
	binary.BigEndian.PutUint32(buf[0:4], Magic)
	binary.BigEndian.PutUint32(buf[4:8], in.Track)
	binary.BigEndian.PutUint32(buf[8:12], in.Source)
	binary.BigEndian.PutUint32(buf[12:16], in.Seq)
	binary.BigEndian.PutUint64(buf[16:24], uint64(in.EventTicks))
	binary.BigEndian.PutUint32(buf[24:28], uint32(len(in.Payload)))
	buf[28] = byte(in.FrameType)
	binary.BigEndian.PutUint32(buf[32:36], uint32(in.Base.Num))
	binary.BigEndian.PutUint32(buf[36:40], uint32(in.Base.Den))
	copy(buf[HeaderSize:HeaderSize+len(in.Payload)], in.Payload)
	sum := crc32.ChecksumIEEE(buf[:HeaderSize+len(in.Payload)])
	binary.BigEndian.PutUint32(buf[len(buf)-crcSize:], sum)
	if in.Corrupt && len(in.Payload) > 0 {
		buf[HeaderSize] ^= 0xFF
	}
	return buf, nil
}

// Reader parses frame records sequentially, tracking byte offsets and the
// global input size ceiling. Invalid frames are returned, not dropped.
type Reader struct {
	r        io.Reader
	stream   string
	consumed int64
}

// NewReader wraps r for the named stream.
func NewReader(r io.Reader, stream string) *Reader {
	return &Reader{r: r, stream: stream}
}

// ReadAll consumes every record until io.EOF. A truncated trailing record or
// an over-long input is returned as a *model.CodeError alongside any frames
// already recovered.
func (rd *Reader) ReadAll() ([]*model.Frame, error) {
	var frames []*model.Frame
	header := make([]byte, HeaderSize)
	for {
		n, err := io.ReadFull(rd.r, header)
		rd.consumed += int64(n)
		if err == io.EOF {
			return frames, nil // clean boundary between records
		}
		if err == io.ErrUnexpectedEOF {
			return frames, &model.CodeError{
				Kind:     model.KindTruncatedBox,
				Offset:   model.RawOffset{Stream: rd.stream, Start: rd.consumed - int64(n), End: rd.consumed},
				FirstPos: rd.consumed - int64(n),
				Message:  "truncated frame record header",
			}
		}
		if err != nil {
			return frames, err
		}
		if rd.consumed > model.MaxTotalInput {
			return frames, model.LengthError(
				model.RawOffset{Stream: rd.stream, Start: rd.consumed - HeaderSize, End: rd.consumed},
				"total input exceeds limit")
		}
		if binary.BigEndian.Uint32(header[0:4]) != Magic {
			return frames, &model.CodeError{
				Kind:    model.KindLength,
				Offset:  model.RawOffset{Stream: rd.stream, Start: rd.consumed - HeaderSize, End: rd.consumed},
				Message: "bad record magic",
			}
		}
		f, perr := rd.readRecord(header)
		if perr != nil {
			return frames, perr
		}
		frames = append(frames, f)
	}
}

func (rd *Reader) readRecord(header []byte) (*model.Frame, error) {
	track := binary.BigEndian.Uint32(header[4:8])
	source := binary.BigEndian.Uint32(header[8:12])
	seq := binary.BigEndian.Uint32(header[12:16])
	ticks := int64(binary.BigEndian.Uint64(header[16:24]))
	payloadLen := binary.BigEndian.Uint32(header[24:28])
	ftype := model.FrameType(header[28])
	base := model.Rational{
		Num: uint64(binary.BigEndian.Uint32(header[32:36])),
		Den: uint64(binary.BigEndian.Uint32(header[36:40])),
	}
	recStart := rd.consumed - HeaderSize
	if payloadLen > model.MaxFramePayload {
		return nil, model.LengthError(
			model.RawOffset{Stream: rd.stream, Start: recStart, End: recStart + HeaderSize},
			"frame payload exceeds limit")
	}
	total := int64(payloadLen) + crcSize
	if recStart+HeaderSize+total > model.MaxTotalInput {
		return nil, model.LengthError(
			model.RawOffset{Stream: rd.stream, Start: recStart, End: recStart + HeaderSize},
			"total input exceeds limit")
	}
	body := make([]byte, total)
	n, err := io.ReadFull(rd.r, body)
	rd.consumed += int64(n)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return nil, &model.CodeError{
			Kind:     model.KindTruncatedBox,
			Track:    track,
			Source:   source,
			Seq:      seq,
			Offset:   model.RawOffset{Stream: rd.stream, Start: recStart, End: rd.consumed},
			FirstPos: recStart,
			Message:  "truncated frame record body",
		}
	}
	if err != nil {
		return nil, err
	}
	payload := body[:payloadLen]
	wantCRC := binary.BigEndian.Uint32(body[payloadLen:])
	hasher := crc32.NewIEEE()
	_, _ = hasher.Write(header)
	_, _ = hasher.Write(payload)
	gotCRC := hasher.Sum32()
	off := model.RawOffset{Stream: rd.stream, Start: recStart, End: rd.consumed}
	f := &model.Frame{
		Track:      track,
		Source:     source,
		Seq:        seq,
		EventTime:  model.NewTimestamp(ticks, base, off),
		PayloadLen: payloadLen,
		Type:       ftype,
		Offset:     off,
		CRC:        wantCRC,
		Valid:      gotCRC == wantCRC,
	}
	if !f.Valid {
		f.Err = model.ChecksumError(off, track, source, seq)
	}
	return f, nil
}
