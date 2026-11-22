package parse

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"

	"archive/internal/model"
)

// The MXF support is limited to a structured KLV metadata subset used
// by the archived sample files. It is not a general MXF/codec decoder.
// Keys are 16-byte private-use ULs: 06 0E 2B 34 ... 7F 01 <item> 01.
var mxfKeyPrefix = []byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0D, 0x01, 0x02, 0x01, 0x7F, 0x01}

const (
	mxfItemHeader  = 0x01 // track header
	mxfItemSamples = 0x02 // sample table entries
	mxfItemColor   = 0x03 // one colour description (may repeat)
	mxfItemCrop    = 0x04 // crop window
	mxfItemRTP     = 0x05 // RTP sync packet
)

// MXFHeader is the decoded track-header KLV value.
type MXFHeader struct {
	TrackID       uint32
	EditRateNum   uint32
	EditRateDen   uint32
	MediaOffset   int64
	Width, Height uint32
	Rotation      uint32 // degrees: 0, 90, 180, 270
	EssenceOffset uint64
}

// ParseMXFFile reads and parses one local MXF sample file.
func ParseMXFFile(path string) (*Track, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > model.MaxInputBytes {
		return nil, model.ParseFault(0, -1, "input exceeds 2 GiB limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseMXF(path, data)
}

// ParseMXF parses the supported KLV subset. An unknown key reports its
// raw offset and stops the track; a truncated value does the same.
func ParseMXF(name string, data []byte) (*Track, error) {
	tr := &Track{Source: name, Format: "mxf"}
	var hdr *MXFHeader
	var sampleEntries []byte
	var essenceOff int64 = -1
	off := int64(0)
	for off < int64(len(data)) {
		// The essence region begins at the offset declared by the
		// header KLV; metadata parsing stops there.
		if hdr != nil && off == int64(hdr.EssenceOffset) {
			break
		}
		klvOff := off
		if int64(len(data))-off < 17 {
			return nil, model.ParseFault(klvOff, -1, "truncated KLV key")
		}
		key := data[off : off+16]
		if !bytes.Equal(key[:14], mxfKeyPrefix) || key[15] != 0x01 {
			return nil, model.ParseFault(klvOff, -1, "unknown KLV key %x: stopping track", key)
		}
		item := key[14]
		off += 16
		ln, n, err := berLength(data[off:])
		if err != nil {
			return nil, model.ParseFault(off, -1, "malformed BER length: %v", err)
		}
		off += int64(n)
		if ln < 0 || ln > int64(len(data))-off {
			return nil, model.ParseFault(klvOff, -1, "KLV value length %d exceeds remaining input", ln)
		}
		val := data[off : off+ln]
		switch item {
		case mxfItemHeader:
			if len(val) != 40 {
				return nil, model.ParseFault(klvOff, -1, "bad header KLV length %d", len(val))
			}
			hdr = &MXFHeader{
				TrackID:       be32(val[0:]),
				EditRateNum:   be32(val[4:]),
				EditRateDen:   be32(val[8:]),
				MediaOffset:   int64(binary.BigEndian.Uint64(val[12:])),
				Width:         be32(val[20:]),
				Height:        be32(val[24:]),
				Rotation:      be32(val[28:]),
				EssenceOffset: binary.BigEndian.Uint64(val[32:]),
			}
		case mxfItemSamples:
			if len(val)%9 != 0 {
				return nil, model.ParseFault(klvOff, -1, "sample KLV length %d not a multiple of 9", len(val))
			}
			sampleEntries = val
		case mxfItemColor:
			if len(val) != 6 {
				return nil, model.ParseFault(klvOff, -1, "bad colour KLV length %d", len(val))
			}
			tr.Colors = append(tr.Colors, ColorInfo{
				Primaries: binary.BigEndian.Uint16(val[0:]),
				Transfer:  binary.BigEndian.Uint16(val[2:]),
				Matrix:    binary.BigEndian.Uint16(val[4:]),
			})
		case mxfItemCrop:
			if len(val) != 16 {
				return nil, model.ParseFault(klvOff, -1, "bad crop KLV length %d", len(val))
			}
			tr.Crop = &Crop{X: be32(val[0:]), Y: be32(val[4:]), W: be32(val[8:]), H: be32(val[12:])}
			if tr.Crop.X+tr.Crop.W > 0 && hdr != nil &&
				(tr.Crop.X+tr.Crop.W > hdr.Width || tr.Crop.Y+tr.Crop.H > hdr.Height || tr.Crop.W == 0 || tr.Crop.H == 0) {
				return nil, model.ParseFault(klvOff, -1, "malformed crop rectangle in KLV")
			}
		case mxfItemRTP:
			ts, err := ParseRTPPacket(val, klvOff, int(hdrID(hdr)))
			if err != nil {
				return nil, err
			}
			tr.RTPTimestamp, tr.HasRTP = ts, true
		default:
			return nil, model.ParseFault(klvOff, -1, "unknown KLV item 0x%02x: stopping track", item)
		}
		off += ln
	}
	if hdr == nil {
		return nil, model.ParseFault(0, -1, "missing track header KLV")
	}
	if hdr.EditRateDen == 0 || hdr.EditRateNum == 0 {
		return nil, model.ParseFault(0, -1, "invalid edit rate %d/%d", hdr.EditRateNum, hdr.EditRateDen)
	}
	if hdr.Width > model.MaxFrameDim || hdr.Height > model.MaxFrameDim {
		return nil, model.ParseFault(0, -1, "frame dimensions exceed %d", model.MaxFrameDim)
	}
	if hdr.EssenceOffset > uint64(len(data)) {
		return nil, model.ParseFault(0, -1, "essence offset beyond input")
	}
	essenceOff = int64(hdr.EssenceOffset)
	tr.TrackID = int(hdr.TrackID)
	if int64(hdr.EditRateNum)%int64(hdr.EditRateDen) != 0 {
		return nil, model.ParseFault(0, int(hdr.TrackID),
			"non-integer edit rate %d/%d not supported by the subset", hdr.EditRateNum, hdr.EditRateDen)
	}
	tr.TimeScale = int64(hdr.EditRateNum) / int64(hdr.EditRateDen)
	tr.MediaOffset = hdr.MediaOffset
	tr.Width, tr.Height = hdr.Width, hdr.Height
	m, err := MatrixForRotation(hdr.Rotation)
	if err != nil {
		return nil, model.ParseFault(0, int(hdr.TrackID), "%v", err)
	}
	tr.Matrix = m
	// Sample table: entries of (duration u32, size u32, keyframe u8).
	var dts int64
	pos := essenceOff
	for i := 0; i+9 <= len(sampleEntries); i += 9 {
		dur := int64(be32(sampleEntries[i:]))
		size := be32(sampleEntries[i+4:])
		kf := sampleEntries[i+8] != 0
		if pos < 0 || pos+int64(size) > int64(len(data)) {
			return nil, model.ParseFault(pos, int(hdr.TrackID),
				"sample %d essence [%d,+%d) exceeds input size %d", i/9, pos, size, len(data))
		}
		tr.Samples = append(tr.Samples, Sample{
			Index: uint32(i / 9), DTS: dts, Duration: dur,
			Offset: pos, Size: size, Keyframe: kf,
		})
		dts += dur
		pos += int64(size)
	}
	if len(tr.Samples) == 0 {
		return nil, model.ParseFault(0, -1, "empty sample table")
	}
	return tr, nil
}

func hdrID(h *MXFHeader) uint32 {
	if h == nil {
		return 0
	}
	return h.TrackID
}

func be32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

// berLength decodes a BER length (short form or long form).
func berLength(b []byte) (int64, int, error) {
	if len(b) == 0 {
		return 0, 0, errBER
	}
	if b[0] < 0x80 {
		return int64(b[0]), 1, nil
	}
	n := int(b[0] & 0x7F)
	if n == 0 || n > 8 || len(b) < 1+n {
		return 0, 0, errBER
	}
	var v int64
	for i := 0; i < n; i++ {
		v = v<<8 | int64(b[1+i])
	}
	return v, 1 + n, nil
}

var errBER = errors.New("invalid BER length")

// MatrixForRotation converts whole-degree rotation to a 3x3 fixed
// point (16.16) display matrix matching the MP4 tkhd convention.
func MatrixForRotation(deg uint32) ([9]int32, error) {
	const one = 0x10000
	switch deg {
	case 0:
		return [9]int32{one, 0, 0, 0, one, 0, 0, 0, 0x40000000}, nil
	case 90:
		return [9]int32{0, one, 0, -one, 0, 0, 0, 0, 0x40000000}, nil
	case 180:
		return [9]int32{-one, 0, 0, 0, -one, 0, 0, 0, 0x40000000}, nil
	case 270:
		return [9]int32{0, -one, 0, one, 0, 0, 0, 0, 0x40000000}, nil
	}
	return [9]int32{}, &model.Fault{Stage: model.StageParse, Track: -1, Offset: -1,
		Msg: "unsupported rotation degrees"}
}
