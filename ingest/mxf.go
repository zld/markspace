// MXF support is deliberately limited to the SMPTE 336M KLV subset emitted
// by the sample fixtures: partition packs, preface, source packages and a
// CDCI picture descriptor carrying rotation/crop/color items. Everything is
// parsed with the Go standard library; no third-party MXF code is used.
package ingest

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"

	"rtpreplay/media"
	"rtpreplay/meta"
)

// 16-byte SMPTE universal labels used by the sample subset.
var (
	keyPartitionPack = ul(0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x02, 0x01, 0x10, 0x01, 0x02, 0x01, 0x00)
	keyPreface       = ul(0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01, 0x2f, 0x00)
	keySourcePackage = ul(0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01, 0x24, 0x00)
	keyCDCI          = ul(0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01, 0x47, 0x01)
	// Local item labels (sample-defined, under the 06 0e 2b 34 01 designator).
	keyRotation = ul(0x06, 0x0e, 0x2b, 0x34, 0x01, 0x01, 0x09, 0x01, 0x01, 0x01, 0x01, 0x01, 0x05, 0x00, 0x00, 0x00)
	keyCrop     = ul(0x06, 0x0e, 0x2b, 0x34, 0x01, 0x01, 0x09, 0x01, 0x01, 0x01, 0x01, 0x01, 0x06, 0x00, 0x00, 0x00)
	keyColor    = ul(0x06, 0x0e, 0x2b, 0x34, 0x01, 0x01, 0x09, 0x01, 0x01, 0x01, 0x01, 0x01, 0x07, 0x00, 0x00, 0x00)
)

func ul(b ...byte) [16]byte {
	var k [16]byte
	copy(k[:], b)
	return k
}

// MXFTrack is one picture track extracted from an MXF sample file.
type MXFTrack struct {
	TrackID    uint32
	Width      uint32
	Height     uint32
	Candidates []meta.Candidate
}

// MXFMetadata is the full MXF parse result.
type MXFMetadata struct {
	Tracks []MXFTrack
}

// ReadMXFFile parses an MXF file path with total-size bounds checking.
func ReadMXFFile(path string) (*MXFMetadata, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, media.NewError(media.KindBadInput, "STAT_FAILED", "mxf", -1, "%v", err)
	}
	if info.Size() > media.MaxInputBytes {
		return nil, media.NewError(media.KindBadInput, "INPUT_TOO_LARGE", "mxf", -1,
			"file %d bytes exceeds cap %d", info.Size(), media.MaxInputBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, media.NewError(media.KindBadInput, "OPEN_FAILED", "mxf", -1, "%v", err)
	}
	defer f.Close()
	return ReadMXF(f, info.Size())
}

type klvReader struct {
	r        io.Reader
	consumed int64
	size     int64
}

// ReadMXF parses the bounded KLV sample subset. Truncated items and out-of-
// range BER lengths produce locatable errors at the offending offset.
func ReadMXF(r io.Reader, size int64) (*MXFMetadata, error) {
	if size > media.MaxInputBytes {
		return nil, media.NewError(media.KindBadInput, "INPUT_TOO_LARGE", "mxf", -1,
			"input %d bytes exceeds cap %d", size, media.MaxInputBytes)
	}
	kr := &klvReader{r: r, size: size}
	md := &MXFMetadata{}
	sawPartition, sawPreface := false, false
	for kr.consumed < size {
		off := kr.consumed
		key, val, err := kr.readKLV()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch key {
		case keyPartitionPack:
			sawPartition = true
		case keyPreface:
			sawPreface = true
		case keySourcePackage:
			tr, err := parseSourcePackage(val)
			if err != nil {
				return nil, media.NewError(media.KindBadInput, err.Code, "mxf", off, err.Detail)
			}
			md.Tracks = append(md.Tracks, *tr)
		default:
			// Unknown top-level KLV items in the subset are skipped.
		}
	}
	if !sawPartition {
		return nil, media.NewError(media.KindBadInput, "NO_PARTITION_PACK", "mxf", 0, "missing MXF partition pack")
	}
	if !sawPreface {
		return nil, media.NewError(media.KindBadInput, "NO_PREFACE", "mxf", -1, "missing MXF preface")
	}
	if len(md.Tracks) == 0 {
		return nil, media.NewError(media.KindBadInput, "NO_TRACKS", "mxf", -1, "no source packages found")
	}
	return md, nil
}

func parseSourcePackage(val []byte) (*MXFTrack, *media.LocatableError) {
	kr := &klvReader{r: bytes.NewReader(val), size: int64(len(val))}
	tr := &MXFTrack{TrackID: 1}
	for kr.consumed < kr.size {
		key, item, err := kr.readKLV()
		if err == io.EOF {
			break
		}
		if err != nil {
			if le, ok := media.AsLocatable(err); ok {
				return nil, le
			}
			return nil, media.NewError(media.KindBadInput, "BAD_KLV", "mxf", -1, "%v", err)
		}
		switch key {
		case keyCDCI:
			if lerr := parseCDCI(item, tr); lerr != nil {
				return nil, lerr
			}
		case keyRotation:
			if len(item) < 4 {
				return nil, media.NewError(media.KindBadInput, "BAD_ROTATION_ITEM", "mxf", -1, "short rotation item")
			}
			rot := meta.Rotation{Degrees: int(binary.BigEndian.Uint32(item[:4]))}
			if verr := meta.ValidateRotation(rot); verr != nil {
				if le, ok := media.AsLocatable(verr); ok {
					return nil, le
				}
				return nil, media.NewError(media.KindBadInput, "BAD_ROTATION", "mxf", -1, "%v", verr)
			}
			tr.Candidates = append(tr.Candidates, meta.Candidate{
				Field: meta.FieldRotation, Source: "mxf:rotation",
				Priority: meta.PriorityMXF, Rotation: &rot,
			})
		case keyCrop:
			if len(item) < 24 {
				return nil, media.NewError(media.KindBadInput, "BAD_CROP_ITEM", "mxf", -1, "short crop item")
			}
			crop := &meta.Crop{}
			vals := make([]uint32, 6)
			for i := range vals {
				vals[i] = binary.BigEndian.Uint32(item[i*4:])
			}
			crop.Left, crop.Top, crop.Right, crop.Bottom, crop.Width, crop.Height = vals[0], vals[1], vals[2], vals[3], vals[4], vals[5]
			tr.Candidates = append(tr.Candidates, meta.Candidate{
				Field: meta.FieldCrop, Source: "mxf:crop",
				Priority: meta.PriorityMXF, Crop: crop,
			})
		}
	}
	return tr, nil
}

func parseCDCI(item []byte, tr *MXFTrack) *media.LocatableError {
	if len(item) < 10 {
		return media.NewError(media.KindBadInput, "BAD_CDCI", "mxf", -1, "CDCI descriptor shorter than 10 bytes")
	}
	tr.Width = binary.BigEndian.Uint32(item[0:4])
	tr.Height = binary.BigEndian.Uint32(item[4:8])
	// Remaining bytes: nclx color triplet (3 x uint16) + full-range flag.
	if len(item) >= 15 {
		c := &meta.Color{
			ColourType:              "nclx",
			ColourPrimaries:         binary.BigEndian.Uint16(item[8:10]),
			TransferCharacteristics: binary.BigEndian.Uint16(item[10:12]),
			MatrixCoefficients:      binary.BigEndian.Uint16(item[12:14]),
			FullRange:               item[14] != 0,
		}
		if verr := meta.ValidateColor(c); verr != nil {
			if le, ok := media.AsLocatable(verr); ok {
				return le
			}
			return media.NewError(media.KindBadInput, "BAD_COLOR", "mxf", -1, "%v", verr)
		}
		tr.Candidates = append(tr.Candidates, meta.Candidate{
			Field: meta.FieldColor, Source: "mxf:cdci",
			Priority: meta.PriorityMXF, Color: c,
		})
	}
	return nil
}

// readKLV reads one key-length-value triple. BER lengths are checked against
// the remaining declared input before any allocation happens.
func (k *klvReader) readKLV() ([16]byte, []byte, error) {
	var key [16]byte
	off := k.consumed
	n, err := io.ReadFull(k.r, key[:])
	k.consumed += int64(n)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		if n == 0 && err == io.EOF {
			return key, nil, io.EOF
		}
		return key, nil, media.NewError(media.KindBadInput, "TRUNCATED_KLV_KEY", "mxf", off,
			"%d of 16 key bytes remain", n)
	}
	if err != nil {
		return key, nil, err
	}
	length, lerr := k.readBERLength()
	if lerr != nil {
		return key, nil, lerr
	}
	remaining := k.size - k.consumed
	if int64(length) > remaining {
		return key, nil, media.NewError(media.KindBadInput, "LENGTH_OUT_OF_BOUNDS", "mxf", off,
			"declared %d value bytes but %d remain", length, remaining)
	}
	if int64(length) > media.MaxInputBytes {
		return key, nil, media.NewError(media.KindBadInput, "LENGTH_OUT_OF_BOUNDS", "mxf", off,
			"value %d bytes exceeds cap", length)
	}
	val := make([]byte, length)
	if n, err := io.ReadFull(k.r, val); err != nil {
		k.consumed += int64(n)
		return key, nil, media.NewError(media.KindBadInput, "TRUNCATED_KLV_VALUE", "mxf", off,
			"want %d value bytes, got %d", length, n)
	}
	k.consumed += int64(length)
	return key, val, nil
}

// readBERLength implements the SMPTE BER long/short form used by the subset.
func (k *klvReader) readBERLength() (uint64, error) {
	var first [1]byte
	off := k.consumed
	n, err := io.ReadFull(k.r, first[:])
	k.consumed += int64(n)
	if err != nil {
		return 0, media.NewError(media.KindBadInput, "TRUNCATED_BER_LENGTH", "mxf", off, "%v", err)
	}
	b := first[0]
	if b < 0x80 {
		return uint64(b), nil
	}
	octets := int(b & 0x7f)
	if octets == 0 || octets > 8 {
		return 0, media.NewError(media.KindBadInput, "BAD_BER_LENGTH", "mxf", off, "BER octet count %d", octets)
	}
	buf := make([]byte, octets)
	if n, err := io.ReadFull(k.r, buf); err != nil {
		k.consumed += int64(n)
		return 0, media.NewError(media.KindBadInput, "TRUNCATED_BER_LENGTH", "mxf", off, "%v", err)
	}
	k.consumed += int64(octets)
	var length uint64
	for _, v := range buf {
		length = length<<8 | uint64(v)
	}
	return length, nil
}
