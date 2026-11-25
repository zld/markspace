// Package mp4 parses generated MP4 fragment/box boundaries using go-mp4.
package mp4

import (
	"bytes"
	"encoding/binary"
	"fmt"

	gomp4 "github.com/abema/go-mp4"

	"mediapack/internal/fault"
)

// FragmentInfo describes the box-level boundaries needed by the packer.
type FragmentInfo struct {
	MoofOffset     uint64 `json:"moof_offset"`
	MoofSize       uint64 `json:"moof_size"`
	MdatOffset     uint64 `json:"mdat_offset"`
	MdatSize       uint64 `json:"mdat_size"`
	MdatPayloadOff uint64 `json:"mdat_payload_offset"`
	DecodeTime     uint64 `json:"base_media_decode_time"`
	Boxes          []Box  `json:"boxes"`
}

// Box is a top-level MP4 box span.
type Box struct {
	Type   string `json:"type"`
	Offset uint64 `json:"offset"`
	Size   uint64 `json:"size"`
}

func box(boxType string, payload []byte) []byte {
	size := uint32(8 + len(payload))
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], size)
	copy(out[4:8], boxType)
	copy(out[8:], payload)
	return out
}

func fullBox(version byte, payload []byte) []byte {
	out := make([]byte, 4+len(payload))
	out[0] = version
	copy(out[4:], payload)
	return out
}

// BuildFragment builds a small moof/traf/tfdt + mdat fragment. These files are
// boundary fixtures for the local media kernel, not arbitrary playable MP4s.
func BuildFragment(sequence uint32, decodeTime uint64, payload []byte) ([]byte, error) {
	mfhdPayload := make([]byte, 4)
	binary.BigEndian.PutUint32(mfhdPayload, sequence)
	mfhd := box("mfhd", fullBox(0, mfhdPayload))

	tfhdPayload := make([]byte, 4)
	binary.BigEndian.PutUint32(tfhdPayload, 1) // track ID
	tfhd := box("tfhd", fullBox(0, tfhdPayload))

	tfdtValue := make([]byte, 8)
	binary.BigEndian.PutUint64(tfdtValue, decodeTime)
	tfdt := box("tfdt", fullBox(1, tfdtValue))

	traf := box("traf", append(tfhd, tfdt...))
	moof := box("moof", append(mfhd, traf...))
	mdat := box("mdat", payload)

	var out bytes.Buffer
	// styp gives the generated segment a standard fragment-compatible brand.
	stypPayload := append([]byte("msdh"), []byte{0, 0, 0, 0}...)
	stypPayload = append(stypPayload, []byte("msdh")...)
	if _, err := out.Write(box("styp", stypPayload)); err != nil {
		return nil, err
	}
	if _, err := out.Write(moof); err != nil {
		return nil, err
	}
	if _, err := out.Write(mdat); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// AnalyzeFragment parses top-level spans and the tfdt decode time.
func AnalyzeFragment(source string, data []byte) (*FragmentInfo, error) {
	r := bytes.NewReader(data)
	info := &FragmentInfo{}
	_, err := gomp4.ReadBoxStructure(r, func(h *gomp4.ReadHandle) (any, error) {
		if len(h.Path) == 1 {
			info.Boxes = append(info.Boxes, Box{
				Type:   h.BoxInfo.Type.String(),
				Offset: h.BoxInfo.Offset,
				Size:   h.BoxInfo.Size,
			})
			switch h.BoxInfo.Type.String() {
			case "moof":
				info.MoofOffset = h.BoxInfo.Offset
				info.MoofSize = h.BoxInfo.Size
			case "mdat":
				info.MdatOffset = h.BoxInfo.Offset
				info.MdatSize = h.BoxInfo.Size
				info.MdatPayloadOff = h.BoxInfo.Offset + h.BoxInfo.HeaderSize
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, fault.Wrap(fault.InvalidMP4, err, "parse MP4 boxes in %s", source)
	}
	if info.MoofSize == 0 || info.MdatSize == 0 {
		return nil, fault.At(fault.InvalidMP4, source, 0, "fragment lacks moof or mdat box")
	}
	if info.MoofOffset >= info.MdatOffset {
		return nil, fault.At(fault.InvalidMP4, source, int64(info.MoofOffset), "moof must precede mdat")
	}
	if int(info.MdatOffset+info.MdatSize) != len(data) {
		return nil, fault.At(fault.InvalidMP4, source, int64(info.MdatOffset),
			"mdat end %d does not match fragment size %d", info.MdatOffset+info.MdatSize, len(data))
	}

	boxes, err := gomp4.ExtractBoxWithPayload(bytes.NewReader(data), nil,
		gomp4.BoxPath{gomp4.BoxTypeMoof(), gomp4.BoxTypeTraf(), gomp4.BoxTypeTfdt()})
	if err != nil {
		return nil, fault.Wrap(fault.InvalidMP4, err, "extract tfdt from %s", source)
	}
	if len(boxes) != 1 {
		return nil, fault.At(fault.InvalidMP4, source, int64(info.MoofOffset),
			"expected one tfdt, got %d", len(boxes))
	}
	tfdt, ok := boxes[0].Payload.(*gomp4.Tfdt)
	if !ok {
		return nil, fault.At(fault.InvalidMP4, source, int64(boxes[0].Info.Offset),
			"tfdt payload has unexpected type %T", boxes[0].Payload)
	}
	info.DecodeTime = tfdt.GetBaseMediaDecodeTime()
	return info, nil
}

// CorruptDecodeTime rewrites the version-1 tfdt value in an already built
// fragment. It is used by the deterministic boundary-failure demo.
func CorruptDecodeTime(data []byte, newTime uint64) error {
	idx := bytes.Index(data, []byte("tfdt"))
	if idx < 0 || idx+4+4+8 > len(data) {
		return fmt.Errorf("tfdt box not found")
	}
	// "tfdt" starts at box type; payload begins 4 bytes later: version+flags.
	valuePos := idx + 4 + 4
	binary.BigEndian.PutUint64(data[valuePos:valuePos+8], newTime)
	return nil
}
