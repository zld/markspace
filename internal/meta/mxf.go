package meta

import (
	"encoding/binary"
	"errors"
	"fmt"

	"mediad/internal/clock"
	"mediad/internal/samplegen"
)

// ParseMXF reads the length-delimited KLV subset emitted by samplegen:
// 16-byte local-set keys, BER-encoded definite lengths and 2-byte local
// tags. A truncated final item (or any short value) is reported as a
// recoverable error; complete track sets parsed before it are returned.
func ParseMXF(data []byte) ([]*TrackDescription, error) {
	var descs []*TrackDescription
	var errAcc error
	pos := 0
	for pos < len(data) {
		if len(data)-pos < 16 {
			errAcc = errors.Join(errAcc, clock.NewError(clock.StageMXF,
				fmt.Sprintf("truncated KLV key at byte %d", pos), true))
			break
		}
		var key [16]byte
		copy(key[:], data[pos:pos+16])
		pos += 16
		length, n, err := readBER(data, pos)
		if err != nil {
			errAcc = errors.Join(errAcc, clock.WrapError(clock.StageMXF, 0, true, err))
			break
		}
		pos += n
		if len(data)-pos < length {
			errAcc = errors.Join(errAcc, clock.NewError(clock.StageMXF,
				fmt.Sprintf("truncated KLV value: need %d bytes, have %d", length, len(data)-pos), true))
			break
		}
		if key == samplegen.MXFLocalSetKey() {
			desc, perr := parseLocalSet(data[pos : pos+length])
			if perr != nil {
				errAcc = errors.Join(errAcc, perr)
			} else {
				descs = append(descs, desc)
			}
		}
		pos += length
	}
	return descs, errAcc
}

// parseLocalSet decodes one track descriptor local set.
func parseLocalSet(value []byte) (*TrackDescription, error) {
	d := &TrackDescription{Container: KindMXF}
	pos := 0
	for pos < len(value) {
		if len(value)-pos < 2 {
			return nil, clock.NewError(clock.StageMXF, "truncated local tag", true)
		}
		tag := binary.BigEndian.Uint16(value[pos:])
		pos += 2
		length, n, err := readBER(value, pos)
		if err != nil {
			return nil, clock.WrapError(clock.StageMXF, 0, true, err)
		}
		pos += n
		if len(value)-pos < length {
			return nil, clock.NewError(clock.StageMXF, "truncated local-tag value", true)
		}
		raw := value[pos : pos+length]
		pos += length
		if perr := applyTag(d, tag, raw); perr != nil {
			return nil, perr
		}
	}
	return d, nil
}

func applyTag(d *TrackDescription, tag uint16, raw []byte) error {
	switch tag {
	case samplegen.MXFTagTrackID:
		if len(raw) != 4 {
			return badTag(tag, len(raw), 4)
		}
		d.TrackID = binary.BigEndian.Uint32(raw)
	case samplegen.MXFTagEditRate:
		if len(raw) != 8 {
			return badTag(tag, len(raw), 8)
		}
		d.Timescale = binary.BigEndian.Uint32(raw[0:])
		d.EditRateDen = binary.BigEndian.Uint32(raw[4:])
	case samplegen.MXFTagWidth:
		if len(raw) != 4 {
			return badTag(tag, len(raw), 4)
		}
		d.Width = binary.BigEndian.Uint32(raw)
	case samplegen.MXFTagHeight:
		if len(raw) != 4 {
			return badTag(tag, len(raw), 4)
		}
		d.Height = binary.BigEndian.Uint32(raw)
	case samplegen.MXFTagRotation:
		if len(raw) != 2 {
			return badTag(tag, len(raw), 2)
		}
		quarters := binary.BigEndian.Uint16(raw) % 4
		d.Rotation = &Rotation{Degrees: int32(quarters) * 90}
	case samplegen.MXFTagCrop:
		if len(raw) != 16 {
			return badTag(tag, len(raw), 16)
		}
		d.Crop = &Crop{
			X:      int64(binary.BigEndian.Uint32(raw[0:])),
			Y:      int64(binary.BigEndian.Uint32(raw[4:])),
			Width:  int64(binary.BigEndian.Uint32(raw[8:])),
			Height: int64(binary.BigEndian.Uint32(raw[12:])),
		}
	case samplegen.MXFTagColour:
		if len(raw) != 7 {
			return badTag(tag, len(raw), 7)
		}
		d.Colour = &Colour{
			Primaries: binary.BigEndian.Uint16(raw[0:]),
			Transfer:  binary.BigEndian.Uint16(raw[2:]),
			Matrix:    binary.BigEndian.Uint16(raw[4:]),
			FullRange: raw[6] != 0,
		}
	default:
		// Unknown tags are skipped, not fatal.
	}
	return nil
}

func badTag(tag uint16, got, want int) error {
	return clock.NewError(clock.StageMXF,
		fmt.Sprintf("tag 0x%04x value length %d, want %d", tag, got, want), true)
}

// readBER decodes an ASN.1 BER definite length and returns the length plus
// the number of header bytes consumed.
func readBER(data []byte, pos int) (int, int, error) {
	if pos >= len(data) {
		return 0, 0, clock.NewError(clock.StageMXF, "missing BER length", true)
	}
	first := data[pos]
	if first < 0x80 {
		return int(first), 1, nil
	}
	count := int(first & 0x7F)
	if count == 0 || count > 4 || pos+1+count > len(data) {
		return 0, 0, clock.NewError(clock.StageMXF,
			fmt.Sprintf("invalid BER length header 0x%02x", first), true)
	}
	length := 0
	for i := 1; i <= count; i++ {
		length = length<<8 | int(data[pos+i])
	}
	return length, 1 + count, nil
}
