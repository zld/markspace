package stream

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"

	"mediapipe/internal/model"
)

// RawRecord is one framed record plus best-effort parsed packets.
type RawRecord struct {
	Header        RecordHeader
	ByteOffset    int64
	PayloadOffset int64
	Payload       []byte
	Corrupt       bool
	CRCError      error
	ParseError    error
	RTPPacket     *rtp.Packet
	RTCPPackets   []rtcp.Packet
}

// Reader parses the framed stream and preserves byte/packet offsets.
type Reader struct {
	r           io.Reader
	byteOffset  int64
	recordIndex int64
}

func ReadFileHeader(r io.Reader) (FileHeader, error) {
	var h FileHeader
	buf := make([]byte, FileHeaderSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return h, model.NewError(model.ErrorHeader, 0, "short stream header", err)
		}
		return h, model.NewError(model.ErrorHeader, 0, "read stream header", err)
	}
	if !bytes.Equal(buf[0:8], StreamMagic[:]) {
		return h, model.NewError(model.ErrorHeader, 0, "bad stream magic", nil)
	}
	h.Magic = StreamMagic
	h.Version = binary.LittleEndian.Uint16(buf[8:10])
	h.HeaderSize = binary.LittleEndian.Uint16(buf[10:12])
	h.ClockRate = binary.LittleEndian.Uint32(buf[12:16])
	h.Flags = binary.LittleEndian.Uint32(buf[16:20])
	if h.Version != FileVersion {
		return h, model.NewError(model.ErrorHeader, 0, "unsupported stream version", nil)
	}
	if h.HeaderSize != FileHeaderSize {
		return h, model.NewError(model.ErrorHeader, 0, "unsupported header size", nil)
	}
	return h, nil
}

func NewReader(r io.Reader) (*Reader, FileHeader, error) {
	h, err := ReadFileHeader(r)
	if err != nil {
		return nil, h, err
	}
	return &Reader{r: r, byteOffset: int64(FileHeaderSize)}, h, nil
}

func (r *Reader) Next() (*RawRecord, error) {
	headerStart := r.byteOffset
	hb := make([]byte, RecordHeaderSize)
	n, err := io.ReadFull(r.r, hb)
	if err != nil {
		if errors.Is(err, io.EOF) && n == 0 {
			return nil, io.EOF
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, model.NewError(model.ErrorTruncated, headerStart, "truncated record header", err)
		}
		return nil, model.NewError(model.ErrorTruncated, headerStart, "read record header", err)
	}
	r.byteOffset += int64(n)

	rec := &RawRecord{ByteOffset: headerStart, PayloadOffset: r.byteOffset}
	rec.Header.Type = hb[0]
	rec.Header.Flags = hb[1]
	rec.Header.Reserved = binary.LittleEndian.Uint16(hb[2:4])
	rec.Header.PayloadLen = binary.LittleEndian.Uint32(hb[4:8])
	rec.Header.CRC32 = binary.LittleEndian.Uint32(hb[8:12])
	rec.Header.PacketOffset = binary.LittleEndian.Uint64(hb[12:20])

	if rec.Header.PayloadLen > 16*1024*1024 {
		return nil, model.NewError(model.ErrorCorrupt, headerStart, "implausible payload length", nil)
	}

	payload := make([]byte, rec.Header.PayloadLen)
	n, err = io.ReadFull(r.r, payload)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			me := model.NewError(model.ErrorTruncated, r.byteOffset+int64(n), "truncated record payload", err)
			me.PacketOffset = int64(rec.Header.PacketOffset)
			return nil, me
		}
		return nil, model.NewError(model.ErrorCorrupt, r.byteOffset, "read record payload", err)
	}
	r.byteOffset += int64(n)
	rec.Payload = payload

	if crc32.ChecksumIEEE(payload) != rec.Header.CRC32 || rec.Header.Flags&FlagCorrupt != 0 {
		rec.Corrupt = true
		rec.CRCError = model.NewError(model.ErrorCorrupt, rec.PayloadOffset, "payload CRC mismatch", nil)
	}

	switch rec.Header.Type {
	case RecordRTP:
		pkt := &rtp.Packet{}
		if perr := pkt.Unmarshal(payload); perr != nil {
			rec.Corrupt = true
			rec.ParseError = model.NewError(model.ErrorCorrupt, rec.PayloadOffset, "invalid RTP packet", perr)
		} else {
			rec.RTPPacket = pkt
		}
	case RecordRTCP:
		pkts, perr := rtcp.Unmarshal(payload)
		if perr != nil {
			rec.Corrupt = true
			rec.ParseError = model.NewError(model.ErrorCorrupt, rec.PayloadOffset, "invalid RTCP packet", perr)
		} else {
			rec.RTCPPackets = pkts
		}
	default:
		rec.Corrupt = true
		rec.ParseError = model.NewError(model.ErrorCorrupt, rec.ByteOffset, "unknown record type", nil)
	}

	r.recordIndex++
	return rec, nil
}

// ReadAll is a convenience for tests and small bounded inputs.
func ReadAll(r io.Reader) (FileHeader, []*RawRecord, error) {
	rd, h, err := NewReader(r)
	if err != nil {
		return h, nil, err
	}
	var out []*RawRecord
	for {
		rec, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return h, out, nil
		}
		if err != nil {
			return h, out, err
		}
		out = append(out, rec)
	}
}
