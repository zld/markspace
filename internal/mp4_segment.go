package kernel

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"

	"github.com/abema/go-mp4"
)

type mp4MediaPayload struct {
	SourceDigest string   `json:"source_digest"`
	Segment      *Segment `json:"segment"`
}

// PackageSegmentMP4 derives a small deterministic ISO-BMFF file containing
// ftyp and mdat boxes. The mdat carries the canonical segment representation.
func PackageSegmentMP4(seg *Segment, sourceDigest string) ([]byte, error) {
	if seg == nil {
		return nil, &MediaError{Stage: "package", Code: "nil_segment", Recoverable: false}
	}
	mediaSeg := *seg
	mediaSeg.FileSHA256 = ""
	mediaSeg.SizeBytes = 0
	media, err := json.Marshal(mp4MediaPayload{SourceDigest: sourceDigest, Segment: &mediaSeg})
	if err != nil {
		return nil, &MediaError{Stage: "package", Code: "encode_mdat", Err: err, Recoverable: false}
	}

	var ftypPayload bytes.Buffer
	ftyp := &mp4.Ftyp{
		MajorBrand:       mp4.BrandISOM(),
		MinorVersion:     0x00000200,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: mp4.BrandISOM()}, {CompatibleBrand: mp4.BrandMP41()}},
	}
	if _, err := mp4.Marshal(&ftypPayload, ftyp, mp4.Context{}); err != nil {
		return nil, &MediaError{Stage: "package", Code: "encode_ftyp", Err: err, Recoverable: false}
	}
	ftypBox, err := boxWithHeader("ftyp", ftypPayload.Bytes())
	if err != nil {
		return nil, err
	}

	var mdatPayload bytes.Buffer
	if _, err := mp4.Marshal(&mdatPayload, &mp4.Mdat{Data: media}, mp4.Context{}); err != nil {
		return nil, &MediaError{Stage: "package", Code: "encode_mdat_box", Err: err, Recoverable: false}
	}
	mdatBox, err := boxWithHeader("mdat", mdatPayload.Bytes())
	if err != nil {
		return nil, err
	}
	return append(ftypBox, mdatBox...), nil
}

func boxWithHeader(boxType string, payload []byte) ([]byte, error) {
	if len(boxType) != 4 {
		return nil, &MediaError{Stage: "package", Code: "box_type", Message: "box type must be four characters", Recoverable: false}
	}
	total := uint64(8) + uint64(len(payload))
	if total > math.MaxUint32 {
		return nil, &MediaError{Stage: "package", Code: "box_too_large", Recoverable: false}
	}
	out := make([]byte, 8, total)
	binary.BigEndian.PutUint32(out[0:4], uint32(total))
	copy(out[4:8], boxType)
	return append(out, payload...), nil
}
