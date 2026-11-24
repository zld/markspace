// Package stream parses MP4 fragmented media and RTP packet streams,
// extracting structure, frame rate, keyframes and byte offsets while
// preserving the original offsets needed to localize corruption.
package stream

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/abema/go-mp4"

	"mediascan/internal/fault"
)

// MaxSegmentSize and MaxBoxSize cap memory allocation before any buffer
// is grown, so a corrupt length field cannot trigger huge allocations.
const (
	MaxSegmentSize = 512 << 20 // 512 MiB
	MaxBoxSize     = 256 << 20 // 256 MiB
)

// BoxInfo records one parsed ISO BMFF box with its original offsets.
type BoxInfo struct {
	Type       string `json:"type"`
	Offset     int64  `json:"offset"`
	HeaderSize int64  `json:"header_size"`
	Size       int64  `json:"declared_size"`
	Depth      int    `json:"depth"`
}

// PayloadOffset returns the first payload byte of the box.
func (b BoxInfo) PayloadOffset() int64 { return b.Offset + b.HeaderSize }

// Sample is one decoded media sample with global and box-local offsets.
type Sample struct {
	Index      int    `json:"index"`
	TrackID    uint32 `json:"track_id"`
	Duration   uint32 `json:"duration"`
	Size       uint32 `json:"size"`
	Keyframe   bool   `json:"keyframe"`
	DTS        uint64 `json:"dts"`         // track timescale units, tfdt + cumulative durations
	ByteOffset int64  `json:"byte_offset"` // original offset inside the segment
}

// TrackInfo summarizes one track's time base.
type TrackInfo struct {
	TrackID   uint32 `json:"track_id"`
	Timescale uint32 `json:"timescale"`
}

// MP4Result is the structured parse output for one segment.
type MP4Result struct {
	Boxes    []BoxInfo   `json:"boxes"`
	Samples  []Sample    `json:"samples"`
	Tracks   []TrackInfo `json:"tracks"`
	MediaSeq uint32      `json:"media_seq"`
	BaseDTS  uint64      `json:"base_dts"`
	// SafePrefix is the count of bytes parsed without fault.
	SafePrefix int64 `json:"safe_prefix"`
	// UnsafeBoundary marks the first byte that cannot be trusted; -1
	// when the whole segment parsed cleanly.
	UnsafeBoundary int64 `json:"unsafe_boundary"`
	Truncated      bool  `json:"truncated"`
}

// TruncatedError carries the original offset of the cut box.
type TruncatedError struct {
	BoxType   string
	Offset    int64
	Declared  int64
	Available int64
}

func (e *TruncatedError) Error() string {
	return fmt.Sprintf("stream: truncated %s box at offset %d: declared %d bytes, %d available",
		e.BoxType, e.Offset, e.Declared, e.Available)
}

// ErrOversizedBox reports a length field that exceeds the safety cap.
var ErrOversizedBox = errors.New("stream: box length exceeds safety limit")

// ParseMP4 walks every box in a fragmented MP4 segment. On truncation it
// returns the partial result together with a *TruncatedError so callers
// keep the verified prefix and the unsafe boundary.
func ParseMP4(data []byte) (*MP4Result, error) {
	if len(data) > MaxSegmentSize {
		return nil, fmt.Errorf("stream: segment size %d exceeds limit %d", len(data), MaxSegmentSize)
	}
	r := &MP4Result{UnsafeBoundary: -1}
	if err := walkBoxes(data, 0, 0, r); err != nil {
		return r, err
	}
	r.SafePrefix = int64(len(data))
	return r, nil
}

func walkBoxes(data []byte, base int64, depth int, r *MP4Result) error {
	pos := 0
	for pos+8 <= len(data) {
		start := base + int64(pos)
		size32 := binary.BigEndian.Uint32(data[pos : pos+4])
		boxType := string(data[pos+4 : pos+8])
		header := 8
		size := int64(size32)
		switch size32 {
		case 0: // box extends to end of input
			size = int64(len(data) - pos)
		case 1: // 64-bit largesize
			if pos+16 > len(data) {
				return r.fail(data, boxType, start, 16)
			}
			size = int64(binary.BigEndian.Uint64(data[pos+8 : pos+16]))
			header = 16
		}
		if size < int64(header) || size > MaxBoxSize {
			return r.fail(data, boxType, start, size)
		}
		if int64(pos)+size > int64(len(data)) {
			return r.fail(data, boxType, start, size)
		}
		info := BoxInfo{Type: boxType, Offset: start, HeaderSize: int64(header),
			Size: size, Depth: depth}
		r.Boxes = append(r.Boxes, info)

		payload := data[pos+header : pos+int(size)]
		if err := parsePayload(boxType, payload, start+int64(header), r); err != nil {
			return err
		}
		if isContainer(boxType) {
			if err := walkBoxes(payload, start+int64(header), depth+1, r); err != nil {
				return err
			}
		}
		pos += int(size)
	}
	if pos < len(data) {
		// Trailing bytes shorter than a header: truncated top-level box.
		return r.fail(data, "????", base+int64(pos), int64(len(data)-pos))
	}
	return nil
}

func (r *MP4Result) fail(data []byte, boxType string, offset, declared int64) error {
	r.Truncated = true
	r.UnsafeBoundary = offset
	r.SafePrefix = offset
	avail := int64(len(data)) - offset
	return &TruncatedError{BoxType: boxType, Offset: offset,
		Declared: declared, Available: avail}
}

func isContainer(t string) bool {
	switch t {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "moof", "traf", "edts":
		return true
	}
	return false
}

// RecordFromTruncation converts a parse error into a fault.Record anchored
// at the original byte offset, using only the segment name (never a host
// path) so the report stays portable.
func RecordFromTruncation(segment string, seq uint64, err error) fault.Record {
	rec := fault.Record{
		Stage: fault.StageParse, Severity: fault.SeverityError,
		Segment: segment, MediaSeq: seq,
	}
	var te *TruncatedError
	if AsTruncated(err, &te) {
		rec.Kind = fault.KindTruncatedBox
		rec.ByteOffset = te.Offset
		rec.SafeBoundary = te.Offset
		rec.Message = te.Error()
		return rec
	}
	rec.Kind = fault.KindRoundtripMismatch
	rec.Message = err.Error()
	return rec
}

// AsTruncated is the errors.As wrapper kept here to centralize the type.
func AsTruncated(err error, target **TruncatedError) bool {
	for err != nil {
		if t, ok := err.(*TruncatedError); ok {
			*target = t
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// boxTypeMP4 maps our string codes onto go-mp4's typed constants so the
// library validates that we only treat known structures as containers.
var boxTypeMP4 = map[string]mp4.BoxType{
	"moof": mp4.BoxTypeMoof(), "traf": mp4.BoxTypeTraf(),
	"tfhd": mp4.BoxTypeTfhd(), "tfdt": mp4.BoxTypeTfdt(),
	"trun": mp4.BoxTypeTrun(), "mdat": mp4.BoxTypeMdat(),
	"mfhd": mp4.BoxTypeMfhd(), "styp": mp4.BoxTypeStyp(),
	"mdhd": mp4.BoxTypeMdhd(), "mvhd": mp4.BoxTypeMvhd(),
}

// ValidateWithLibrary re-parses the segment through go-mp4's own box
// reader and returns the boxes it recognized. It is the independent
// structural cross-check used by the validate stage; an error means the
// round-trip structure is not trustworthy.
func ValidateWithLibrary(data []byte) ([]string, error) {
	var seen []string
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		if _, _, err := h.ReadPayload(); err != nil {
			return nil, err
		}
		seen = append(seen, h.BoxInfo.Type.String())
		return h.Expand()
	}
	if _, err := mp4.ReadBoxStructure(bytes.NewReader(data), handler); err != nil {
		return seen, err
	}
	return seen, nil
}
