package ingest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"

	"github.com/abema/go-mp4"

	"mediapack/model"
)

// MP4Sample is one generated sample carried in a small, explicitly supported
// "samp" box. The project fixtures use this box in addition to a standard ftyp.
type MP4Sample struct {
	Index          int    `json:"index"`
	PTSNanos       int64  `json:"pts_nanos"`
	DurationNanos  int64  `json:"duration_nanos"`
	Keyframe       bool   `json:"keyframe"`
	Payload        []byte `json:"-"`
	Offset         int64  `json:"offset"`
	BoxSize        uint32 `json:"box_size"`
	IntegrityCRC32 uint32 `json:"crc32"`
}

// BoxRecord is the subset of go-mp4 BoxInfo retained as evidence.
type BoxRecord struct {
	Offset     uint64 `json:"offset"`
	Size       uint64 `json:"size"`
	HeaderSize uint64 `json:"header_size"`
	Type       string `json:"type"`
}

// InspectBoxes uses go-mp4 to enumerate top-level boxes in generated fixtures.
func InspectBoxes(data []byte) ([]BoxRecord, error) {
	var records []BoxRecord
	_, err := mp4.ReadBoxStructure(bytes.NewReader(data), func(h *mp4.ReadHandle) (interface{}, error) {
		records = append(records, BoxRecord{
			Offset:     h.BoxInfo.Offset,
			Size:       h.BoxInfo.Size,
			HeaderSize: h.BoxInfo.HeaderSize,
			Type:       h.BoxInfo.Type.String(),
		})
		return nil, nil
	})
	return records, err
}

// WriteFTYP uses go-mp4's marshaller for a standard supported box.
func WriteFTYP() ([]byte, error) {
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'M', 'P', 'A', 'C'},
		MinorVersion: 1,
	}
	ftyp.CompatibleBrands = append(ftyp.CompatibleBrands, mp4.CompatibleBrandElem{
		CompatibleBrand: [4]byte{'m', 'p', '4', '2'},
	})
	var payload bytes.Buffer
	n, err := mp4.Marshal(&payload, ftyp, mp4.Context{})
	if err != nil {
		return nil, err
	}
	full := make([]byte, 8+payload.Len())
	binary.BigEndian.PutUint32(full[0:4], uint32(len(full)))
	copy(full[4:8], "ftyp")
	copy(full[8:], payload.Bytes())
	_ = n
	return full, nil
}

// BuildSampleBox encodes one supported sample box.
func BuildSampleBox(s MP4Sample) []byte {
	if len(s.Payload) == 0 {
		s.Payload = []byte(fmt.Sprintf("sample-%d", s.Index))
	}
	boxSize := uint32(8 + 24 + len(s.Payload))
	b := make([]byte, boxSize)
	binary.BigEndian.PutUint32(b[0:4], boxSize)
	copy(b[4:8], "samp")
	b[8] = 1 // version
	if s.Keyframe {
		b[9] = 0x01
	}
	// b[10:12] reserved.
	binary.BigEndian.PutUint64(b[12:20], uint64(s.PTSNanos))
	binary.BigEndian.PutUint64(b[20:28], uint64(s.DurationNanos))
	crc := crc32.ChecksumIEEE(s.Payload)
	binary.BigEndian.PutUint32(b[28:32], crc)
	copy(b[32:], s.Payload)
	return b
}

// BuildFixtureMP4 builds ftyp followed by sample boxes.
func BuildFixtureMP4(samples []MP4Sample) ([]byte, error) {
	ftyp, err := WriteFTYP()
	if err != nil {
		return nil, model.NewError("video", "ftyp", 0, model.CategoryOutputWrite, "cannot marshal ftyp", err)
	}
	var out bytes.Buffer
	out.Write(ftyp)
	for _, s := range samples {
		out.Write(BuildSampleBox(s))
	}
	return out.Bytes(), nil
}

// ParseSampleBoxes validates the supported sample boxes. A CRC-damaged box with
// an intact length is skipped; truncation returns the verified prefix plus a
// recoverable categorized error.
func ParseSampleBoxes(track model.TrackID, data []byte) ([]MP4Sample, []model.Warning, error) {
	var samples []MP4Sample
	var warnings []model.Warning
	offset := int64(0)
	for offset < int64(len(data)) {
		boxOffset := offset
		if len(data)-int(offset) < 8 {
			err := model.NewError(track, "mp4-box", offset, model.CategoryTruncatedBox,
				fmt.Sprintf("only %d header bytes remain", len(data)-int(offset)), nil).RecoverableWarn()
			return samples, warnings, err
		}
		size := int64(binary.BigEndian.Uint32(data[offset : offset+4]))
		boxType := string(data[offset+4 : offset+8])
		if size < 8 {
			err := model.NewError(track, "mp4-box:"+boxType, offset, model.CategoryInvalidLength,
				fmt.Sprintf("box size %d is smaller than 8-byte header", size), nil)
			return samples, warnings, err
		}
		if offset+size > int64(len(data)) {
			err := model.NewError(track, "mp4-box:"+boxType, offset, model.CategoryTruncatedBox,
				fmt.Sprintf("box declares %d bytes but only %d remain; eof_offset=%d",
					size, int64(len(data))-offset, len(data)), nil).RecoverableWarn()
			return samples, warnings, err
		}
		if boxType != "samp" {
			offset += size
			continue
		}
		if size < 32 {
			err := model.NewError(track, "mp4-box:samp", offset, model.CategoryInvalidLength,
				fmt.Sprintf("sample box size %d is shorter than fixed fields", size), nil)
			return samples, warnings, err
		}

		flags := data[offset+9]
		pts := int64(binary.BigEndian.Uint64(data[offset+12 : offset+20]))
		duration := int64(binary.BigEndian.Uint64(data[offset+20 : offset+28]))
		wantCRC := binary.BigEndian.Uint32(data[offset+28 : offset+32])
		payload := append([]byte(nil), data[offset+32:offset+size]...)
		gotCRC := crc32.ChecksumIEEE(payload)
		if wantCRC != gotCRC {
			w := model.Warning{
				Track: track, Offset: offset, Category: model.CategoryIntegrityMismatch,
				Message: fmt.Sprintf("skipping sample box: crc32 expected %08x got %08x", wantCRC, gotCRC),
			}
			warnings = append(warnings, w)
			offset += size
			continue
		}
		samples = append(samples, MP4Sample{
			Index: len(samples), PTSNanos: pts, DurationNanos: duration,
			Keyframe: flags&0x01 == 1, Payload: payload, Offset: boxOffset,
			BoxSize: uint32(size), IntegrityCRC32: wantCRC,
		})
		offset += size
	}
	return samples, warnings, nil
}

// TruncateAt cuts data inside the box containing targetOffset. It returns the
// cut position and the containing box offset for diagnostic output.
func TruncateAt(data []byte, targetOffset int) (cutData []byte, cutOffset int, boxOffset int64, err error) {
	if targetOffset < 8 || targetOffset >= len(data) {
		return nil, 0, -1, fmt.Errorf("truncate target %d outside fixture", targetOffset)
	}
	off := int64(0)
	for off+8 <= int64(len(data)) {
		size := int64(binary.BigEndian.Uint32(data[off : off+4]))
		if size < 8 || off+size > int64(len(data)) {
			return nil, 0, off, fmt.Errorf("fixture already invalid at %d", off)
		}
		end := off + size
		if int64(targetOffset) >= off && int64(targetOffset) < end {
			// Cut after the 8-byte header and at least one payload byte.
			cut := targetOffset
			if cut <= int(off)+8 {
				cut = int(off) + 9
			}
			if cut >= len(data) {
				cut = len(data) - 1
			}
			return append([]byte(nil), data[:cut]...), cut, off, nil
		}
		off = end
	}
	return nil, 0, -1, fmt.Errorf("no box contains target offset %d", targetOffset)
}
