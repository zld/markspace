package media

import (
	"fmt"

	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/internal/mxfschema"
)

// This file decodes the regression-profile descriptor and index local sets.

func mxfDescription(fields []localSetField, set klvEntry) (*Track, *Description, error) {
	priorityField, err := field(fields, mxfschema.TagPriority, 0, set.ValueOff)
	if err != nil {
		return nil, nil, err
	}
	priority, err := priorityField.uintValue(0)
	if err != nil {
		return nil, nil, err
	}
	idField, err := field(fields, mxfschema.TagTrackID, 0, set.ValueOff)
	if err != nil {
		return nil, nil, err
	}
	id, err := idField.uintValue(0)
	if err != nil {
		return nil, nil, err
	}
	kindField, err := field(fields, mxfschema.TagTrackKind, uint32(id), set.ValueOff)
	if err != nil {
		return nil, nil, err
	}
	kind := Kind(kindField.stringValue())
	if !kind.Valid() {
		return nil, nil, merr.New(merr.StageMetadata, merr.KindParse, uint32(id), kindField.Off,
			"mxf: unsupported track kind %q", string(kindField.Value))
	}
	tsField, err := field(fields, mxfschema.TagTimescale, uint32(id), set.ValueOff)
	if err != nil {
		return nil, nil, err
	}
	timescale, err := tsField.uintValue(uint32(id))
	if err != nil {
		return nil, nil, err
	}
	durField, err := field(fields, mxfschema.TagDuration, uint32(id), set.ValueOff)
	if err != nil {
		return nil, nil, err
	}
	duration, err := durField.uintValue(uint32(id))
	if err != nil {
		return nil, nil, err
	}
	desc := &Description{Priority: int(priority), Origin: fmt.Sprintf("mxf-descriptor[%d]", priority)}
	track := &Track{ID: uint32(id), Kind: kind, Timescale: uint32(timescale), Duration: int64(duration), InputOffset: set.Offset}
	switch kind {
	case KindVideo:
		if err := mxfVideoDescription(fields, desc, uint32(id)); err != nil {
			return nil, nil, err
		}
	case KindAudio:
		if err := mxfAudioDescription(fields, desc, uint32(id)); err != nil {
			return nil, nil, err
		}
	}
	return track, desc, nil
}

func mxfVideoDescription(fields []localSetField, desc *Description, id uint32) error {
	width, err := mxfUint(fields, mxfschema.TagWidth, id)
	if err != nil {
		return err
	}
	height, err := mxfUint(fields, mxfschema.TagHeight, id)
	if err != nil {
		return err
	}
	desc.Width, desc.Height = uint32(width), uint32(height)
	rot, err := mxfUint(fields, mxfschema.TagRotation, id)
	if err != nil {
		return err
	}
	r := int(rot)
	desc.Rotation = &r
	cw, err := mxfUint(fields, mxfschema.TagCropWidth, id)
	if err != nil {
		return err
	}
	ch, err := mxfUint(fields, mxfschema.TagCropHeight, id)
	if err != nil {
		return err
	}
	cx, err := mxfUint(fields, mxfschema.TagCropX, id)
	if err != nil {
		return err
	}
	cy, err := mxfUint(fields, mxfschema.TagCropY, id)
	if err != nil {
		return err
	}
	desc.Crop = &Crop{Width: uint32(cw), Height: uint32(ch), HorizOffset: int32(cx), VertOffset: int32(cy)}
	p, err := mxfUint(fields, mxfschema.TagPrimaries, id)
	if err != nil {
		return err
	}
	tr, err := mxfUint(fields, mxfschema.TagTransfer, id)
	if err != nil {
		return err
	}
	m, err := mxfUint(fields, mxfschema.TagMatrix, id)
	if err != nil {
		return err
	}
	fr, err := field(fields, mxfschema.TagFullRange, id, 0)
	if err != nil {
		return err
	}
	full, err := fr.uintValue(id)
	if err != nil {
		return err
	}
	desc.Colour = &Colour{Primaries: uint16(p), Transfer: uint16(tr), Matrix: uint16(m), FullRange: full != 0}
	return nil
}

func mxfAudioDescription(fields []localSetField, desc *Description, id uint32) error {
	rate, err := mxfUint(fields, mxfschema.TagAudioRate, id)
	if err != nil {
		return err
	}
	ch, err := mxfUint(fields, mxfschema.TagAudioChan, id)
	if err != nil {
		return err
	}
	layout, err := field(fields, mxfschema.TagAudioLayout, id, 0)
	if err != nil {
		return err
	}
	desc.Rate = uint32(rate)
	desc.Channels = uint16(ch)
	desc.Layout = layout.stringValue()
	return nil
}

func mxfUint(fields []localSetField, tag uint16, id uint32) (uint64, error) {
	f, err := field(fields, tag, id, 0)
	if err != nil {
		return 0, err
	}
	return f.uintValue(id)
}

// mxfBlock is one decoded audio index entry.
type mxfBlock struct {
	Index      int
	TrackID    uint32
	StartFrame int64
	Frames     int64
	Offset     int64
	Length     int
	CRC32      uint32
}

func mxfIndex(index klvEntry) ([]mxfBlock, error) {
	fields, err := parseLocalSet(index.Value, index.ValueOff)
	if err != nil {
		return nil, err
	}
	countField, err := field(fields, mxfschema.TagBlockCount, 0, index.ValueOff)
	if err != nil {
		return nil, err
	}
	count, err := countField.uintValue(0)
	if err != nil {
		return nil, err
	}
	var blocks []mxfBlock
	for _, f := range fields {
		if f.Tag != mxfschema.TagBlock {
			continue
		}
		entry, err := parseLocalSet(f.Value, f.Off)
		if err != nil {
			return nil, err
		}
		var b mxfBlock
		get := func(tag uint16) (uint64, error) { return mxfUint(entry, tag, 0) }
		if v, err := get(mxfschema.TagBlockNo); err != nil {
			return nil, err
		} else {
			b.Index = int(v)
		}
		if v, err := get(mxfschema.TagTrackID); err != nil {
			return nil, err
		} else {
			b.TrackID = uint32(v)
		}
		if v, err := get(mxfschema.TagStartFrame); err != nil {
			return nil, err
		} else {
			b.StartFrame = int64(v)
		}
		if v, err := get(mxfschema.TagFrames); err != nil {
			return nil, err
		} else {
			b.Frames = int64(v)
		}
		if v, err := get(mxfschema.TagPayloadOff); err != nil {
			return nil, err
		} else {
			b.Offset = int64(v)
		}
		if v, err := get(mxfschema.TagPayloadLen); err != nil {
			return nil, err
		} else {
			b.Length = int(v)
		}
		if v, err := get(mxfschema.TagPayloadCRC); err != nil {
			return nil, err
		} else {
			b.CRC32 = uint32(v)
		}
		blocks = append(blocks, b)
	}
	if uint64(len(blocks)) != count {
		return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, 0, index.ValueOff,
			"mxf: index declares %d blocks but lists %d", count, len(blocks))
	}
	return blocks, nil
}
