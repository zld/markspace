package fixture

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"mediapack/internal/model"
)

// KLV constants mirrored from the probe package's wire format.
var klvPrefix = [4]byte{0x06, 0x0e, 0x2b, 0x34}

const (
	mxfTagHeader = 0x0101
	mxfTagTrack  = 0x0301
)

// buildMXF emits a header partition pack and one 47-byte track set as KLV.
func buildMXF(s TrackSpec, samples int) []byte {
	header := klvItem(mxfTagHeader, []byte("OP1a"))
	track := klvItem(mxfTagTrack, mxfTrackPayload(s, samples))
	return append(header, track...)
}

func klvItem(tag uint16, payload []byte) []byte {
	key := make([]byte, 16)
	copy(key[0:4], klvPrefix[:])
	binary.BigEndian.PutUint16(key[14:16], tag)
	// Short-form BER length (payloads stay under 128 bytes).
	return append(append(key, byte(len(payload))), payload...)
}

// mxfTrackPayload mirrors probe.mxfTrackSetSize (47 bytes) field for field.
func mxfTrackPayload(s TrackSpec, samples int) []byte {
	b := make([]byte, 47)
	p := 0
	binary.BigEndian.PutUint32(b[p:], s.ID)
	p += 4
	copy(b[p:p+4], []byte(handlerFor(s.Kind)))
	p += 4
	binary.BigEndian.PutUint32(b[p:], s.ClockRate)
	p += 4
	binary.BigEndian.PutUint32(b[p:], uint32(samples))
	p += 4
	binary.BigEndian.PutUint64(b[p:], uint64(s.ClockRate)*uint64(s.Seconds))
	p += 8
	binary.BigEndian.PutUint32(b[p:], 1920) // width
	p += 4
	binary.BigEndian.PutUint32(b[p:], 1080) // height
	p += 4
	binary.BigEndian.PutUint32(b[p:], s.ClockRate) // RTP clock rate
	p += 4
	if s.WrapNear {
		binary.BigEndian.PutUint32(b[p:], uint32(0xFFFFFFFF)-s.ClockRate*2)
	} // RTP base
	p += 4
	b[p] = 1 // has RTP anchor
	p++
	binary.BigEndian.PutUint16(b[p:], 1) // primaries
	p += 2
	binary.BigEndian.PutUint16(b[p:], 1) // transfer
	p += 2
	binary.BigEndian.PutUint16(b[p:], 1) // matrix
	return b
}

// Fault describes one injected corruption and the exact byte offset it targets.
type Fault struct {
	Name     string
	Path     string
	Offset   int64 // the injected fault's true byte offset
	WantCode model.Code
}

// InjectTruncatedBox copies src to dst and inflates the second top-level box
// header's declared size so it overruns the file. The returned offset is the
// exact header byte the scanner must flag.
func InjectTruncatedBox(src, dst string) (*Fault, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return nil, err
	}
	firstSize := binary.BigEndian.Uint32(raw[0:4])
	off := int64(firstSize) // start of the next top-level box header
	if int(off)+8 > len(raw) {
		off = 0
	}
	out := append([]byte{}, raw...)
	binary.BigEndian.PutUint32(out[off:], uint32(len(out))+256)
	if err := os.WriteFile(dst, out, 0o600); err != nil {
		return nil, err
	}
	return &Fault{Name: "truncated_box", Path: dst, Offset: off, WantCode: model.CodeTruncatedBox}, nil
}

// InjectIllegalLength writes a box header whose size is smaller than itself.
func InjectIllegalLength(src, dst string) (*Fault, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	out := append([]byte{}, raw...)
	binary.BigEndian.PutUint32(out[0:], 4)
	if err := os.WriteFile(dst, out, 0o600); err != nil {
		return nil, err
	}
	return &Fault{Name: "illegal_length", Path: dst, Offset: 0, WantCode: model.CodeIllegalLength}, nil
}

// InjectTruncatedMXF truncates a KLV key mid-stream.
func InjectTruncatedMXF(src, dst string) (*Fault, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	cut := len(raw) - 8
	if cut < 0 {
		cut = 0
	}
	if err := os.WriteFile(dst, raw[:cut], 0o600); err != nil {
		return nil, err
	}
	return &Fault{Name: "truncated_mxf", Path: dst, Offset: int64(cut), WantCode: model.CodeTruncatedBox}, nil
}

// InjectCorruptIndex writes non-JSON bytes at the start of an index file.
func InjectCorruptIndex(indexPath string) (*Fault, error) {
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(indexPath, []byte("{corrupt-index"), 0o600); err != nil {
		return nil, err
	}
	return &Fault{Name: "corrupt_index", Path: indexPath, Offset: 0, WantCode: model.CodeCorruptIndex}, nil
}

// InjectChangedInput appends one byte so the input summary (size + digest)
// changes and any cached index is invalidated.
func InjectChangedInput(src, dst string) (*Fault, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	out := append(append([]byte{}, raw...), 0x7F)
	if err := os.WriteFile(dst, out, 0o600); err != nil {
		return nil, err
	}
	return &Fault{Name: "changed_input", Path: dst, Offset: int64(len(raw)), WantCode: model.CodeInputChanged}, nil
}
