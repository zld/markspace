package fixture

import (
	"bytes"
	"fmt"

	"github.com/abema/go-mp4"
)

// SegmentInfo is the go-mp4 parsed view of one media segment.
type SegmentInfo struct {
	SequenceNumber  uint32
	BaseMediaDecode uint64 // tfdt base timestamp
	SampleCount     uint32
	MoofOffset      uint64
	MdatSize        uint64
}

// InitInfo is the parsed initialization segment view.
type InitInfo struct {
	Timescale uint32
	TrackID   uint32
}

// ParseInitSegment walks the synthesized init segment with go-mp4 and
// extracts the mdhd timescale, proving the bytes are valid ISO BMFF.
func ParseInitSegment(data []byte) (InitInfo, error) {
	r := bytes.NewReader(data)
	boxes, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
			mp4.BoxTypeMdhd()},
	})
	if err != nil {
		return InitInfo{}, fmt.Errorf("init parse: %w", err)
	}
	if len(boxes) == 0 {
		return InitInfo{}, fmt.Errorf("init segment has no mdhd box")
	}
	payload, ok := boxes[0].Payload.(*mp4.Mdhd)
	if !ok {
		return InitInfo{}, fmt.Errorf("mdhd payload has unexpected type %T",
			boxes[0].Payload)
	}
	return InitInfo{Timescale: payload.Timescale}, nil
}

// ParseMediaSegment walks moof/mdat and reads mfhd sequence, tfdt base
// decode time and the trun sample count.
func ParseMediaSegment(data []byte) (SegmentInfo, error) {
	r := bytes.NewReader(data)
	var info SegmentInfo

	mfhdBoxes, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoof(), mp4.BoxTypeMfhd()},
	})
	if err != nil {
		return info, fmt.Errorf("media parse moof: %w", err)
	}
	if len(mfhdBoxes) == 0 {
		return info, fmt.Errorf("media segment has no mfhd box")
	}
	info.MoofOffset = mfhdBoxes[0].Info.Offset
	if mfhd, ok := mfhdBoxes[0].Payload.(*mp4.Mfhd); ok {
		info.SequenceNumber = mfhd.SequenceNumber
	}

	tfdtBoxes, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTfdt()},
	})
	if err != nil {
		return info, fmt.Errorf("media parse tfdt: %w", err)
	}
	if len(tfdtBoxes) > 0 {
		if p, ok := tfdtBoxes[0].Payload.(*mp4.Tfdt); ok {
			if p.GetVersion() == 1 {
				info.BaseMediaDecode = p.BaseMediaDecodeTimeV1
			} else {
				info.BaseMediaDecode = uint64(p.BaseMediaDecodeTimeV0)
			}
		}
	}

	trunBoxes, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTrun()},
	})
	if err != nil {
		return info, fmt.Errorf("media parse trun: %w", err)
	}
	if len(trunBoxes) > 0 {
		if trun, ok := trunBoxes[0].Payload.(*mp4.Trun); ok {
			info.SampleCount = trun.SampleCount
		}
	}

	mdatBoxes, err := mp4.ExtractBoxes(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeMdat()},
	})
	if err != nil {
		return info, fmt.Errorf("media parse mdat: %w", err)
	}
	if len(mdatBoxes) == 0 {
		return info, fmt.Errorf("media segment has no mdat box")
	}
	info.MdatSize = mdatBoxes[0].Size
	return info, nil
}
