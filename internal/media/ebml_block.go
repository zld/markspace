package media

import (
	"encoding/binary"
	"math"
)

// ExtractBlocks decodes SimpleBlock and BlockGroup/Block elements from a
// successfully parsed document. Malformed block payloads return a structured
// error and are never appended as synthetic events.
func ExtractBlocks(data []byte, doc *Document) ([]*Block, error) {
	scale := uint64(1_000_000)
	for _, info := range doc.FindDescendants(IDInfo) {
		if tc := info.FirstChild(IDTimecodeScale); tc != nil {
			if v, err := ReadUint(data, tc); err == nil && v > 0 {
				scale = v
			}
		}
	}

	var blocks []*Block
	trackSeq := map[uint32]int{}
	globalSeq := 0
	for _, cluster := range doc.FindDescendants(IDCluster) {
		clusterTC := int64(0)
		if tcEl := cluster.FirstChild(IDTimecode); tcEl != nil {
			v, err := ReadUint(data, tcEl)
			if err != nil {
				return blocks, err
			}
			if v > uint64(maxInt64) {
				return blocks, NewError(StageEBML, tcEl.HeaderOffset, tcEl.Path, ErrTimeOverflow)
			}
			clusterTC = int64(v)
		}

		for _, child := range cluster.Children {
			switch child.ID {
			case IDSimpleBlock:
				b, err := decodeBlockPayload(data, child, clusterTC, scale, trackSeq, &globalSeq, true, 0)
				if err != nil {
					return blocks, err
				}
				blocks = append(blocks, b)
			case IDBlockGroup:
				blockEl := child.FirstChild(IDBlock)
				if blockEl == nil {
					return blocks, NewError(StageEBML, child.HeaderOffset, child.Path, ErrTruncatedBlock).
						WithKind("block_group_without_block")
				}
				durationTC := uint64(0)
				if dEl := child.FirstChild(IDBlockDuration); dEl != nil {
					v, err := ReadUint(data, dEl)
					if err != nil {
						return blocks, err
					}
					durationTC = v
				}
				b, err := decodeBlockPayload(data, blockEl, clusterTC, scale, trackSeq, &globalSeq, false, durationTC)
				if err != nil {
					return blocks, err
				}
				blocks = append(blocks, b)
			}
		}
	}
	doc.Blocks = blocks
	return blocks, nil
}

const maxInt64 = int64(^uint64(0) >> 1)

func decodeBlockPayload(
	data []byte,
	el *Element,
	clusterTC int64,
	scale uint64,
	trackSeq map[uint32]int,
	globalSeq *int,
	simple bool,
	durationTC uint64,
) (*Block, error) {
	trackVINT, afterTrack, err := ReadVINT(data, el.DataOffset, el.End, el.Path)
	if err != nil {
		return nil, err
	}
	trackID := uint32(trackVINT.Value)
	if trackID == 0 {
		return nil, NewError(StageEBML, el.HeaderOffset, el.Path, ErrInvalidVINT).
			WithTrack(0).WithKind("zero_track_number")
	}
	if afterTrack+3 > el.End {
		return nil, NewError(StageEBML, el.HeaderOffset, el.Path, ErrTruncatedBlock).
			WithTrack(trackID).WithKind("block_header_needs_timecode_and_flags")
	}
	rel := int16(binary.BigEndian.Uint16(data[afterTrack : afterTrack+2]))
	flags := data[afterTrack+2]
	payloadStart := afterTrack + 3

	clusterNS, err := ScaleMatroskaTimecode(clusterTC, scale)
	if err != nil {
		return nil, NewError(StageEBML, el.HeaderOffset, el.Path, err).WithTrack(trackID)
	}
	relNS, err := ScaleMatroskaTimecode(int64(rel), scale)
	if err != nil {
		return nil, NewError(StageEBML, el.HeaderOffset, el.Path, err).WithTrack(trackID)
	}
	rawPTS, err := CheckedAdd(clusterNS, relNS)
	if err != nil {
		return nil, NewError(StageEBML, el.HeaderOffset, el.Path, err).WithTrack(trackID)
	}

	seq := trackSeq[trackID]
	trackSeq[trackID] = seq + 1
	*globalSeq++
	b := &Block{
		ElementOffset:   el.HeaderOffset,
		DataOffset:      el.DataOffset,
		Path:            el.Path,
		TrackID:         trackID,
		Sequence:        seq,
		GlobalSequence:  *globalSeq - 1,
		ClusterTC:       clusterTC,
		RelativeTC:      rel,
		TimecodeScaleNS: scale,
		RawPTSNS:        rawPTS,
		PTSNS:           rawPTS,
		Keyframe:        simple && flags&0x80 != 0,
		Payload:         append([]byte(nil), data[payloadStart:el.End]...),
	}
	if durationTC > 0 {
		d, err := ScaleMatroskaTimecode(int64(durationTC), scale)
		if err != nil {
			return nil, NewError(StageEBML, el.HeaderOffset, el.Path, err).WithTrack(trackID)
		}
		b.DurationNS = d
	}
	return b, nil
}

// ReadUint decodes an unsigned EBML integer element body.
func ReadUint(data []byte, el *Element) (uint64, error) {
	n := el.End - el.DataOffset
	if n <= 0 || n > 8 {
		return 0, NewError(StageEBML, el.HeaderOffset, el.Path, ErrInvalidVINT).
			WithKind("uint_length")
	}
	var v uint64
	for i := el.DataOffset; i < el.End; i++ {
		v = v<<8 | uint64(data[i])
	}
	return v, nil
}

// ReadString decodes an EBML UTF-8 string body.
func ReadString(data []byte, el *Element) string {
	if el.DataOffset < 0 || el.End > int64(len(data)) || el.End < el.DataOffset {
		return ""
	}
	return string(data[el.DataOffset:el.End])
}

// ReadFloat decodes a 4- or 8-byte EBML float body.
func ReadFloat(data []byte, el *Element) (float64, error) {
	n := el.End - el.DataOffset
	switch n {
	case 4:
		bits := binary.BigEndian.Uint32(data[el.DataOffset:el.End])
		return float64(math.Float32frombits(bits)), nil
	case 8:
		bits := binary.BigEndian.Uint64(data[el.DataOffset:el.End])
		return math.Float64frombits(bits), nil
	default:
		return 0, NewError(StageEBML, el.HeaderOffset, el.Path, ErrInvalidVINT).
			WithKind("float_length")
	}
}
