// Package frame parses and validates versioned image-frame records.
//
// Wire format (all integers big-endian), one record per ParseRecord call:
//
//	magic      4 bytes  "CFR1"
//	version    1 byte   (currently 1)
//	trackLen   2 bytes  + track UTF-8
//	seq        8 bytes  frame sequence number
//	captureNS  8 bytes  capture instant on the canonical nanosecond clock
//	width      4 bytes
//	height     4 bytes
//	color      4 bytes  colorSpace, primaries, transfer, fullRange
//	payloadLen 4 bytes
//	payload    payloadLen bytes
//	checksum   32 bytes SHA-256 over every byte from version through payload
//
// Truncation, contradictory length fields and checksum failures all return
// *fault.Error values carrying the first locatable stream offset; no record
// is ever skipped silently.
package frame

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"

	"example.com/continuity/internal/fault"
)

// Magic prefixes every frame record.
const Magic = "CFR1"

// Version1 is the only supported record version.
const Version1 uint8 = 1

// ChecksumSize is the trailing SHA-256 digest size in bytes.
const ChecksumSize = sha256.Size

// MaxPayload is the hard per-frame payload cap used when the caller does not
// supply its own limit. It keeps a single corrupt length field from causing
// unbounded allocation.
const MaxPayload = 64 << 20 // 64 MiB

const maxTrackLen = 256

// ColorMeta carries the color metadata associated with a frame.
type ColorMeta struct {
	ColorSpace uint8 // 1=BT.601 2=BT.709 3=BT.2020
	Primaries  uint8
	Transfer   uint8
	FullRange  uint8 // 0=limited, 1=full
}

// Record is a parsed frame record.
type Record struct {
	Version   uint8
	Track     string
	Seq       uint64
	CaptureNS int64
	Width     uint32
	Height    uint32
	Color     ColorMeta
	Payload   []byte
	Checksum  [ChecksumSize]byte
	// Offset is the stream byte offset of the record's first byte.
	Offset int64
	// TotalSize is the number of wire bytes the record occupies.
	TotalSize int
}

type cursor struct {
	buf   []byte
	pos   int
	base  int64 // stream offset of buf[0]
	stage string
	track string
	frame int64
}

func (c *cursor) fail(op string, cause error) *fault.Error {
	e := fault.New(fault.StageFrame, op, cause).AtOffset(c.base + int64(c.pos))
	if c.track != "" {
		e.AtTrack(c.track)
	}
	if c.frame >= 0 {
		e.AtFrame(c.frame)
	}
	return e
}

func (c *cursor) take(n int) ([]byte, error) {
	if c.pos+n > len(c.buf) {
		return nil, c.fail("parse", fault.ErrTruncated)
	}
	b := c.buf[c.pos : c.pos+n]
	c.pos += n
	return b, nil
}

func (c *cursor) u8() (uint8, error) {
	b, err := c.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (c *cursor) u16() (uint16, error) {
	b, err := c.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (c *cursor) u32() (uint32, error) {
	b, err := c.take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (c *cursor) u64() (uint64, error) {
	b, err := c.take(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

// ParseRecord parses one record from buf, which must hold exactly one record.
// streamOffset is the byte offset of buf[0] inside the input stream and is
// used to build locatable fault offsets. maxPayload caps the accepted payload
// length; pass 0 to use MaxPayload.
func ParseRecord(buf []byte, streamOffset int64, maxPayload int) (*Record, error) {
	if maxPayload <= 0 {
		maxPayload = MaxPayload
	}
	c := &cursor{buf: buf, base: streamOffset, frame: -1}
	magic, err := c.take(4)
	if err != nil {
		return nil, err
	}
	if string(magic) != Magic {
		return nil, c.fail("magic", errors.New("bad frame record magic"))
	}
	rec := &Record{Offset: streamOffset}
	if rec.Version, err = c.u8(); err != nil {
		return nil, err
	}
	if rec.Version != Version1 {
		return nil, c.fail("version", errors.New("unsupported record version"))
	}
	trackLen, err := c.u16()
	if err != nil {
		return nil, err
	}
	if int(trackLen) > maxTrackLen {
		return nil, c.fail("track", fault.ErrLength)
	}
	raw, err := c.take(int(trackLen))
	if err != nil {
		return nil, err
	}
	rec.Track = string(raw)
	c.track = rec.Track
	seq, err := c.u64()
	if err != nil {
		return nil, err
	}
	rec.Seq = seq
	c.frame = int64(seq)
	v, err := c.u64()
	if err != nil {
		return nil, err
	}
	rec.CaptureNS = int64(v)
	if rec.CaptureNS < 0 {
		return nil, c.fail("timestamp", errors.New("negative capture timestamp"))
	}
	if rec.Width, err = c.u32(); err != nil {
		return nil, err
	}
	if rec.Height, err = c.u32(); err != nil {
		return nil, err
	}
	if rec.Width == 0 || rec.Height == 0 {
		return nil, c.fail("dimensions", errors.New("zero frame dimension"))
	}
	color, err := c.take(4)
	if err != nil {
		return nil, err
	}
	rec.Color = ColorMeta{color[0], color[1], color[2], color[3]}
	payloadLen, err := c.u32()
	if err != nil {
		return nil, err
	}
	if int(payloadLen) > maxPayload {
		return nil, c.fail("payload", fault.ErrLimitExceeded)
	}
	if rec.Payload, err = c.take(int(payloadLen)); err != nil {
		return nil, err
	}
	digest, err := c.take(ChecksumSize)
	if err != nil {
		return nil, err
	}
	copy(rec.Checksum[:], digest)
	if c.pos != len(buf) {
		return nil, c.fail("length", fault.ErrLength)
	}
	// Checksum covers version through payload, i.e. every byte between the
	// 4-byte magic and the trailing digest. The fault is anchored at the
	// digest's first byte: the first locatable checksum position.
	digestOff := c.base + int64(len(buf)-ChecksumSize)
	want := sha256.Sum256(buf[4 : len(buf)-ChecksumSize])
	if want != rec.Checksum {
		return nil, fault.New(fault.StageFrame, "checksum", fault.ErrChecksum).
			AtTrack(rec.Track).AtFrame(int64(rec.Seq)).AtOffset(digestOff)
	}
	rec.TotalSize = c.pos
	return rec, nil
}

// MarshalRecord serializes rec, recomputing its checksum. It is used by the
// fixture generator and by tests; production inputs always arrive preframed.
func MarshalRecord(rec *Record) ([]byte, error) {
	if rec == nil {
		return nil, fault.New(fault.StageFrame, "marshal", errors.New("nil record"))
	}
	if len(rec.Track) > maxTrackLen {
		return nil, fault.New(fault.StageFrame, "track", fault.ErrLength)
	}
	if len(rec.Payload) > MaxPayload {
		return nil, fault.New(fault.StageFrame, "payload", fault.ErrLimitExceeded)
	}
	size := 4 + 1 + 2 + len(rec.Track) + 8 + 8 + 4 + 4 + 4 + 4 + len(rec.Payload) + ChecksumSize
	out := make([]byte, 0, size)
	out = append(out, Magic...)
	out = append(out, Version1)
	out = binary.BigEndian.AppendUint16(out, uint16(len(rec.Track)))
	out = append(out, rec.Track...)
	out = binary.BigEndian.AppendUint64(out, rec.Seq)
	out = binary.BigEndian.AppendUint64(out, uint64(rec.CaptureNS))
	out = binary.BigEndian.AppendUint32(out, rec.Width)
	out = binary.BigEndian.AppendUint32(out, rec.Height)
	out = append(out, rec.Color.ColorSpace, rec.Color.Primaries,
		rec.Color.Transfer, rec.Color.FullRange)
	out = binary.BigEndian.AppendUint32(out, uint32(len(rec.Payload)))
	out = append(out, rec.Payload...)
	sum := sha256.Sum256(out[4:])
	out = append(out, sum[:]...)
	return out, nil
}

// ReadFramed reads one length-prefixed record body from r: the stream framing
// is uint32 body length followed by the record bytes. It returns the body and
// the stream offset at which the length prefix started.
func ReadFramed(r io.Reader, maxRecord int) (body []byte, prefixOffset int64, err error) {
	var lenBuf [4]byte
	if _, err = io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, 0, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if int(n) > maxRecord {
		return nil, 0, fault.New(fault.StageFrame, "frame", fault.ErrLimitExceeded)
	}
	body = make([]byte, n)
	if _, err = io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			err = fault.New(fault.StageFrame, "parse", fault.ErrTruncated)
		}
		return nil, 0, err
	}
	return body, 0, nil
}
