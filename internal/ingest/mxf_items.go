package ingest

import (
	"encoding/binary"

	"mediapack/internal/fixture"
	"mediapack/internal/model"
)

// applyItem maps one KLV value onto the current track fields. Every retained
// value records its KLV provenance.
func (r *mxfReader) applyItem(t *Track, id byte, value []byte, off int, path string) error {
	src := model.SourceOnly(r.file, path, int64(off))
	switch id {
	case fixture.IDKind:
		if len(value) < 4 {
			return model.Truncated(r.file, int64(off), path, "kind value truncated")
		}
		t.Handler = string(value[:4])
		switch t.Handler {
		case "vide":
			t.Kind = TrackVideo
		case "soun":
			t.Kind = TrackAudio
		default:
			t.Kind = TrackKind(t.Handler)
		}
		t.Sources["handler"] = src
	case fixture.IDTimescale:
		v, err := needU32(value, r.file, off, path)
		if err != nil {
			return err
		}
		t.Timescale = v
		t.Sources["timescale"] = src
	case fixture.IDDuration:
		v, err := needU32(value, r.file, off, path)
		if err != nil {
			return err
		}
		t.DurationSrc = uint64(v)
	case fixture.IDWidth:
		v, err := needU16(value, r.file, off, path)
		if err != nil {
			return err
		}
		t.Width = v
		if v > MaxDimension {
			return model.OutOfRange(r.file, int64(off), path,
				"width exceeds 4096 subset limit")
		}
		t.Sources["dimensions"] = src
	case fixture.IDHeight:
		v, err := needU16(value, r.file, off, path)
		if err != nil {
			return err
		}
		t.Height = v
		if v > MaxDimension {
			return model.OutOfRange(r.file, int64(off), path,
				"height exceeds 4096 subset limit")
		}
	case fixture.IDRotation:
		v, err := needI32(value, r.file, off, path)
		if err != nil {
			return err
		}
		t.Rotation = int(v)
		t.Matrix = rotationToMatrix(int(v))
		t.Sources["matrix"] = src
	case fixture.IDCrop:
		if len(value) < 32 {
			return model.Truncated(r.file, int64(off), path,
				"crop value shorter than 32 bytes")
		}
		c := &Crop{
			CleanWidthN:  binary.BigEndian.Uint32(value[0:4]),
			CleanWidthD:  binary.BigEndian.Uint32(value[4:8]),
			CleanHeightN: binary.BigEndian.Uint32(value[8:12]),
			CleanHeightD: binary.BigEndian.Uint32(value[12:16]),
			HorizOffN:    int32(binary.BigEndian.Uint32(value[16:20])),
			HorizOffD:    int32(binary.BigEndian.Uint32(value[20:24])),
			VertOffN:     int32(binary.BigEndian.Uint32(value[24:28])),
			VertOffD:     int32(binary.BigEndian.Uint32(value[28:32])),
		}
		x, y, w, h := c.EffectiveRect(uint32(t.Width), uint32(t.Height))
		legal := w > 0 && h > 0 && x >= 0 && y >= 0 &&
			x+w <= int64(t.Width) && y+h <= int64(t.Height)
		if !legal {
			r.out.Warnings = append(r.out.Warnings, model.NewFieldError(
				model.KindInvalidInput, r.file, int64(off), path,
				"crop rectangle outside frame; falling back to full aperture"))
			return nil
		}
		t.Crop = c
		t.Sources["crop"] = src
	case fixture.IDColor:
		if len(value) < 9 {
			return model.Truncated(r.file, int64(off), path,
				"color value shorter than 9 bytes")
		}
		t.Colors = append(t.Colors, ColorDesc{
			Type:      string(value[0:4]),
			Primaries: binary.BigEndian.Uint16(value[4:6]),
			Transfer:  binary.BigEndian.Uint16(value[6:8]),
			Matrix:    binary.BigEndian.Uint16(value[8:10]),
			FullRange: value[10] != 0,
			Source:    src,
		})
	case fixture.IDChannels:
		v, err := needU16(value, r.file, off, path)
		if err != nil {
			return err
		}
		t.Channels = v
		t.Sources["audio"] = src
	case fixture.IDRate:
		v, err := needU32(value, r.file, off, path)
		if err != nil {
			return err
		}
		t.SampleRate = v
	case fixture.IDSampleSize:
		v, err := needU16(value, r.file, off, path)
		if err != nil {
			return err
		}
		t.SampleSize = v
	case fixture.IDSample:
		if len(value) < 8 {
			return model.Truncated(r.file, int64(off), path,
				"sample value shorter than 8 bytes")
		}
		idx := binary.BigEndian.Uint32(value[0:4])
		delta := binary.BigEndian.Uint32(value[4:8])
		t.Samples = append(t.Samples, Sample{Index: int(idx), Delta: delta})
	default:
		// Unknown subset items are ignored, never fatal.
	}
	return nil
}

// rotationToMatrix mirrors fixture.RotationMatrix so parsed MXF tracks carry
// the same matrix representation as MP4 tracks.
func rotationToMatrix(deg int) [9]int32 {
	const one = 0x00010000
	switch deg {
	case 90:
		return [9]int32{0, one, 0, -one, 0, 0, 0, 0, one}
	case 180:
		return [9]int32{-one, 0, 0, 0, -one, 0, 0, 0, one}
	case 270:
		return [9]int32{0, -one, 0, one, 0, 0, 0, 0, one}
	default:
		return [9]int32{one, 0, 0, 0, one, 0, 0, 0, one}
	}
}

func needU32(v []byte, file string, off int, path string) (uint32, error) {
	if len(v) < 4 {
		return 0, model.Truncated(file, int64(off), path, "uint32 value truncated")
	}
	return binary.BigEndian.Uint32(v), nil
}

func needU16(v []byte, file string, off int, path string) (uint16, error) {
	if len(v) < 2 {
		return 0, model.Truncated(file, int64(off), path, "uint16 value truncated")
	}
	return binary.BigEndian.Uint16(v), nil
}

func needI32(v []byte, file string, off int, path string) (int32, error) {
	u, err := needU32(v, file, off, path)
	return int32(u), err
}
