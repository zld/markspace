// Package ingest parses the supported CMAF/fMP4 subset with exact byte
// offsets. It never trusts a declared box length: every read is checked
// against the real file bounds before any payload is touched.
package ingest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediacore/internal/fault"
)

// ErrEnd marks a clean end of the box sequence.
var ErrEnd = errors.New("ingest: end of input")

// Limits bounds every read and scan. A zero field disables that bound.
type Limits struct {
	MaxFileBytes        int64 `json:"max_file_bytes"`
	MaxBoxes            int   `json:"max_boxes"`
	MaxScanBoxes        int   `json:"max_scan_boxes"`
	MaxFragmentDuration int64 `json:"max_fragment_duration"`
}

// DefaultLimits are conservative bounds for local sample data.
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:        64 << 20,
		MaxBoxes:            1 << 16,
		MaxScanBoxes:        4096,
		MaxFragmentDuration: 10 * 90000,
	}
}

// Header is one boundary-checked box header.
type Header struct {
	Offset       int64  `json:"offset"`
	Size         int64  `json:"size"`
	HeaderSize   int64  `json:"header_size"`
	Type         string `json:"type"`
	ExtendsToEOF bool   `json:"extends_to_eof,omitempty"`
}

// End returns the exclusive end offset of the box.
func (h Header) End() int64 { return h.Offset + h.Size }

// PayloadStart returns the offset of the first payload byte.
func (h Header) PayloadStart() int64 { return h.Offset + h.HeaderSize }

// PayloadSize returns the payload length in bytes.
func (h Header) PayloadSize() int64 { return h.Size - h.HeaderSize }

// TypeString renders a four byte type, escaping non-printable bytes.
func TypeString(b []byte) string {
	if len(b) < 4 {
		return fmt.Sprintf("%x", b)
	}
	printable := true
	for _, c := range b[:4] {
		if c < 0x20 || c > 0x7e {
			printable = false
			break
		}
	}
	if printable {
		return string(b[:4])
	}
	return fmt.Sprintf("%02x%02x%02x%02x", b[0], b[1], b[2], b[3])
}

// Reader provides boundary-safe access to one input file held in memory.
type Reader struct {
	File   string
	Data   []byte
	Limits Limits

	headers int
}

// NewReader enforces the file size bound before any parsing begins.
func NewReader(file string, data []byte, lim Limits) (*Reader, *fault.Error) {
	if lim.MaxFileBytes > 0 && int64(len(data)) > lim.MaxFileBytes {
		return nil, fault.New(fault.CodeLimit, file, 0, "", fmt.Sprintf("file %d bytes exceeds limit %d", len(data), lim.MaxFileBytes))
	}
	if lim.MaxBoxes < 0 || lim.MaxScanBoxes < 0 {
		return nil, fault.New(fault.CodeLimit, file, 0, "", "negative limit")
	}
	return &Reader{File: file, Data: data, Limits: lim}, nil
}

// Len returns the reader length.
func (r *Reader) Len() int64 { return int64(len(r.Data)) }

// Next returns the header at off. ok is false at a clean end of input.
func (r *Reader) Next(off int64) (h Header, ok bool, f *fault.Error) {
	if off == r.Len() {
		return Header{}, false, nil
	}
	if off < 0 || off > r.Len() {
		return Header{}, false, fault.New(fault.CodeTruncatedHeader, r.File, r.Len(), "", fmt.Sprintf("seek %d outside file", off))
	}
	if r.Limits.MaxBoxes > 0 && r.headers >= r.Limits.MaxBoxes {
		return Header{}, false, fault.New(fault.CodeLimit, r.File, off, "", fmt.Sprintf("box count limit %d reached", r.Limits.MaxBoxes))
	}
	remaining := r.Len() - off
	if remaining < SmallHeaderSize {
		// Fewer than eight bytes remain: no type can be read, so none is
		// claimed. The offset is where the partial header starts.
		return Header{}, false, fault.New(fault.CodeTruncatedHeader, r.File, off, "", fmt.Sprintf("box header at %d has %d/%d bytes, input ends at %d", off, remaining, SmallHeaderSize, r.Len()))
	}
	size32 := binary.BigEndian.Uint32(r.Data[off:])
	typ := TypeString(r.Data[off+4:])
	h = Header{Offset: off, Size: int64(size32), HeaderSize: SmallHeaderSize, Type: typ}
	switch {
	case size32 == 0:
		h.Size = r.Len() - off
		h.ExtendsToEOF = true
	case size32 == 1:
		if remaining < LargeHeaderSize {
			return Header{}, false, fault.New(fault.CodeTruncatedHeader, r.File, off, typ, fmt.Sprintf("large box header at %d has %d/%d bytes, input ends at %d", off, remaining, LargeHeaderSize, r.Len()))
		}
		h.Size = int64(binary.BigEndian.Uint64(r.Data[off+SmallHeaderSize:]))
		h.HeaderSize = LargeHeaderSize
		if h.Size < LargeHeaderSize {
			return Header{}, false, fault.New(fault.CodeInvalidBoxSize, r.File, off, typ, fmt.Sprintf("declared size %d < large header %d", h.Size, LargeHeaderSize))
		}
	case size32 < 8:
		return Header{}, false, fault.New(fault.CodeInvalidBoxSize, r.File, off, typ, fmt.Sprintf("declared size %d < header %d", size32, SmallHeaderSize))
	}
	r.headers++
	if h.Size > remaining {
		// The declared extent is not backed by input. The fault is anchored
		// at the header that made the invalid declaration; detail records
		// where the bytes actually stop.
		return h, false, fault.New(fault.CodeTruncatedBox, r.File, off, typ,
			fmt.Sprintf("box at %d declares %d bytes, only %d available, input ends at %d", off, h.Size, remaining, r.Len()))
	}
	return h, true, nil
}

// SmallHeaderSize and LargeHeaderSize are the ISO BMFF header sizes.
const (
	SmallHeaderSize = int64(8)
	LargeHeaderSize = int64(16)
)

// Payload returns the payload slice for a valid header.
func (r *Reader) Payload(h Header) ([]byte, *fault.Error) {
	if h.Offset < 0 || h.Size < h.HeaderSize || h.End() > r.Len() {
		return nil, fault.New(fault.CodeOversizeBox, r.File, h.Offset, h.Type, "declared range exceeds input")
	}
	return r.Data[h.PayloadStart():h.End()], nil
}

// Unmarshal decodes the payload of h with go-mp4. The payload is passed as a
// bounded reader so go-mp4 cannot read past the declared box.
func (r *Reader) Unmarshal(h Header, dst mp4.IBox) *fault.Error {
	payload, f := r.Payload(h)
	if f != nil {
		return f
	}
	n, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), dst, mp4.Context{})
	if err != nil {
		code := fault.CodeBadPayload
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			code = fault.CodeTruncatedBox
		}
		return fault.New(code, r.File, h.Offset, h.Type, err.Error())
	}
	if int64(n) != h.PayloadSize() {
		return fault.New(fault.CodeBadPayload, r.File, h.Offset, h.Type,
			fmt.Sprintf("decoded %d of %d payload bytes", n, h.PayloadSize()))
	}
	return nil
}
