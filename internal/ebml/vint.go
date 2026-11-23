package ebml

import (
	"errors"
	"fmt"
	"io"

	"mediaarchive/internal/timeline"
)

// readElementHeader reads ID and size VINT without crossing the file end.
func (r *Reader) readElementHeader() (Element, error) {
	start := r.pos()
	id, idLen, err := r.readID()
	if err != nil {
		return Element{}, err
	}
	size, sizeLen, unknown, err := r.readSize()
	if err != nil {
		return Element{}, err
	}
	hdrLen := idLen + sizeLen
	payloadOff := start + int64(hdrLen)
	if !unknown && int64(size) > r.limits.MaxElementSize {
		r.report.OversizedElements = append(r.report.OversizedElements, start)
		return Element{}, timeline.NewError("ebml", "read_header", start,
			timeline.RecoverNone,
			fmt.Sprintf("element size %d exceeds cap", size),
			timeline.ErrLimitExceeded)
	}
	el := Element{
		ID: id, Offset: start, HeaderSize: int64(hdrLen),
		PayloadOffset: payloadOff, End: -1,
	}
	if unknown {
		el.Size = -1
	} else {
		el.Size = int64(size)
		el.End = payloadOff + int64(size)
		if el.End > r.size {
			// A truncated tail: keep the element but clamp its boundary to
			// the real EOF so callers can recover the intact prefix.
			el.End = r.size
			el.Truncated = true
		}
	}
	return el, nil
}

func (r *Reader) readID() ([]byte, int, error) {
	first, err := r.readByte()
	if err != nil {
		return nil, 0, err
	}
	width := vintWidth(first)
	if width == 0 || width > 4 {
		return nil, 0, timeline.NewError("ebml", "read_id", r.pos()-1,
			timeline.RecoverNone, "invalid element ID width", timeline.ErrOutOfBounds)
	}
	buf := make([]byte, width)
	buf[0] = first
	if width > 1 {
		if _, err := io.ReadFull(r.r, buf[1:]); err != nil {
			return nil, 0, io.ErrUnexpectedEOF
		}
	}
	return buf, width, nil
}

func (r *Reader) readSize() (uint64, int, bool, error) {
	start := r.pos()
	first, err := r.readByte()
	if err != nil {
		return 0, 0, false, err
	}
	width := vintWidth(first)
	// Matroska/RFC 8794 always encodes unknown size as a four-byte 0xFF
	// run. A leading all-ones byte therefore marks a four-byte VINT.
	if first == 0xFF {
		width = 4
	}
	if width == 0 || width > 8 {
		return 0, 0, false, timeline.NewError("ebml", "read_size", start,
			timeline.RecoverNone, "invalid VINT width", timeline.ErrOutOfBounds)
	}
	raw := make([]byte, width)
	raw[0] = first
	if width > 1 {
		if _, err := io.ReadFull(r.r, raw[1:]); err != nil {
			return 0, 0, false, io.ErrUnexpectedEOF
		}
	}
	// An EBML VINT with every value bit set (the marker bit excluded)
	// encodes an unknown length. This works at any width, including the
	// single-byte 0xFF marker.
	unknown := true
	for i, b := range raw {
		mask := byte(0xFF)
		if i == 0 {
			mask = 0x7F >> uint(width-1) // exclude marker bit on first byte
		}
		if b&mask != mask {
			unknown = false
		}
	}
	val := uint64(raw[0] & (0x7F >> uint(width-1)))
	for _, b := range raw[1:] {
		val = val<<8 | uint64(b)
	}
	if unknown {
		return 0, width, true, nil
	}
	return val, width, false, nil
}

func (r *Reader) readUintPayload(el Element, end int64) (uint64, error) {
	n := end - el.PayloadOffset
	if n <= 0 || n > 8 {
		return 0, timeline.NewError("ebml", "uint_payload", el.Offset,
			timeline.RecoverPartial, "unsupported uint width", timeline.ErrOutOfBounds)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r.r, buf); err != nil {
		return 0, io.ErrUnexpectedEOF
	}
	var v uint64
	for _, b := range buf {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

func (r *Reader) skipPayload(el Element) error {
	if el.End < 0 {
		return nil
	}
	return r.seek(el.End)
}

func (r *Reader) issue(op string, off int64, level timeline.RecoverLevel, detail string, cause error) {
	r.report.Issues = append(r.report.Issues,
		timeline.NewError("ebml", op, off, level, detail, cause))
}

func (r *Reader) fail(op string, off int64, level timeline.RecoverLevel, cause error) error {
	var te *timeline.Error
	if errors.As(cause, &te) {
		te.Subsystem = "ebml"
		te.Op = op
		if te.Offset < 0 {
			te.Offset = off
		}
		te.Level = level
		r.report.Issues = append(r.report.Issues, te)
		return te
	}
	e := timeline.NewError("ebml", op, off, level, cause.Error(), cause)
	r.report.Issues = append(r.report.Issues, e)
	return e
}

func (r *Reader) pos() int64 {
	p, err := r.r.Seek(0, io.SeekCurrent)
	if err != nil {
		panic(err)
	}
	return p
}

func (r *Reader) seek(p int64) error {
	if p > r.size {
		return timeline.NewError("ebml", "seek", p, timeline.RecoverNone,
			"seek past file end", timeline.ErrOutOfBounds)
	}
	_, err := r.r.Seek(p, io.SeekStart)
	return err
}

func (r *Reader) readByte() (byte, error) {
	b := []byte{0}
	n, err := r.r.Read(b)
	if n == 1 {
		return b[0], nil
	}
	if err == nil {
		err = io.EOF
	}
	return 0, err
}

// readVINT decodes an EBML variable-length integer (track numbers etc.).
func readVINT(buf []byte) (uint64, int, error) {
	if len(buf) == 0 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	width := vintWidth(buf[0])
	if width == 0 || width > len(buf) {
		return 0, 0, timeline.NewError("ebml", "read_vint", -1,
			timeline.RecoverNone, "invalid VINT", timeline.ErrOutOfBounds)
	}
	val := uint64(buf[0] & (0x7F >> uint(width-1)))
	for i := 1; i < width; i++ {
		val = val<<8 | uint64(buf[i])
	}
	return val, width, nil
}

func vintWidth(first byte) int {
	for i := 0; i < 8; i++ {
		if first&(0x80>>uint(i)) != 0 {
			return i + 1
		}
	}
	return 0
}
