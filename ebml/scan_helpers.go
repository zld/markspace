package ebml

import (
	"encoding/binary"
	"math/bits"

	"mediacheck/timeline"
)

// element is one parsed child with its absolute header and value bytes.
type element struct {
	header  *Header
	payload []byte
}

func (e element) raw() ([]byte, error) {
	var out []byte
	out = append(out, EncodeID(uint32(e.header.ID))...)
	sz, err := EncodeSize(uint64(len(e.payload)), 0)
	if err != nil {
		return nil, err
	}
	out = append(out, sz...)
	return append(out, e.payload...), nil
}

// childElements splits a known-size payload into its direct child elements.
func childElements(base int64, payload []byte) ([]element, error) {
	var out []element
	pos := 0
	for pos < len(payload) {
		idLen := vintLen(payload[pos])
		if pos+idLen > len(payload) {
			return nil, timeline.New(timeline.CatCorrupt, "child id runs past element").
				WithOffset(base + int64(pos))
		}
		id, _, err := decodeVINT(payload[pos:pos+idLen], true)
		if err != nil {
			return nil, timeline.New(timeline.CatCorrupt, "invalid child id").
				WithOffset(base + int64(pos)).Wrapf(err)
		}
		sp := pos + idLen
		sizeLen := vintLen(payload[sp])
		if sp+sizeLen > len(payload) {
			return nil, timeline.New(timeline.CatCorrupt, "child size runs past element").
				WithOffset(base + int64(sp))
		}
		size, unknown, err := decodeVINT(payload[sp:sp+sizeLen], false)
		if err != nil {
			return nil, timeline.New(timeline.CatCorrupt, "invalid child size").
				WithOffset(base + int64(sp)).Wrapf(err)
		}
		if unknown {
			return nil, timeline.New(timeline.CatUnsupported, "nested unknown-length child").
				WithOffset(base + int64(pos))
		}
		dataStart := sp + sizeLen
		dataEnd := dataStart + int(size)
		if dataEnd > len(payload) || dataEnd < dataStart {
			return nil, timeline.New(timeline.CatCorrupt, "child payload exceeds parent").
				WithOffset(base + int64(pos))
		}
		out = append(out, element{
			header: &Header{
				ID:         ElementID(id),
				Size:       int64(size),
				Offset:     base + int64(pos),
				DataOffset: base + int64(dataStart),
				HeaderLen:  idLen + sizeLen,
			},
			payload: payload[dataStart:dataEnd],
		})
		pos = dataEnd
	}
	return out, nil
}

func parseTrack(e element) (*TrackSpec, error) {
	kids, err := childElements(e.header.DataOffset, e.payload)
	if err != nil {
		return nil, err
	}
	t := &TrackSpec{}
	for _, k := range kids {
		p := k.payload
		switch k.header.ID {
		case IDTrackNumber:
			t.Number = beUint(p)
		case IDTrackUID:
			t.UID = beUint(p)
		case IDTrackType:
			if len(p) < 1 {
				return nil, timeline.New(timeline.CatCorrupt, "empty track type").WithOffset(k.header.Offset)
			}
			t.Type = p[0]
		case IDCodecID:
			t.CodecID = string(p)
		case IDDefaultDur:
			t.DefaultDurNs = int64(beUint(p))
		case IDPixelWidth:
			t.Width = beUint(p)
		case IDPixelHeight:
			t.Height = beUint(p)
		case IDSamplingFreq:
			t.SampleRate = uint32(beUint(p))
		case IDChannels:
			t.Channels = beUint(p)
		case IDBitDepth:
			t.BitDepth = beUint(p)
		case IDChanLayout:
			t.LayoutName = string(p)
		case IDChanMap:
			if len(p)%8 != 0 {
				return nil, timeline.New(timeline.CatCorrupt, "channel map table not 64-bit aligned").
					WithOffset(k.header.Offset)
			}
			for i := 0; i < len(p); i += 8 {
				t.ChannelMap = append(t.ChannelMap, ChannelMapEntry{
					Position: beUint(p[i : i+4]),
					Order:    beUint(p[i+4 : i+8]),
				})
			}
		case IDColour:
			cm, err := parseColorPayload(k)
			if err != nil {
				return nil, err
			}
			t.Color = cm
		}
	}
	return t, nil
}

func parseColor(er *Reader, h *Header) (*ColorMeta, error) {
	payload, err := er.ReadPayload(h)
	if err != nil {
		return nil, err
	}
	return parseColorPayload(element{header: h, payload: payload})
}

func parseColorPayload(k element) (*ColorMeta, error) {
	kids, err := childElements(k.header.DataOffset, k.payload)
	if err != nil {
		return nil, err
	}
	cm := &ColorMeta{SourceElement: k.header.ID, SourceOffset: k.header.Offset}
	for _, c := range kids {
		switch c.header.ID {
		case IDColorMatrix:
			cm.Matrix = beUint(c.payload)
		case IDColorTransfer:
			cm.Transfer = beUint(c.payload)
		case IDColorPrimaries:
			cm.Primaries = beUint(c.payload)
		case IDColorRange:
			cm.RangeFlag = beUint(c.payload)
		case IDColorRaw:
			cm.RawEnum = beUint(c.payload)
		}
	}
	return cm, nil
}

func beUint(p []byte) uint32 {
	switch len(p) {
	case 1:
		return uint32(p[0])
	case 2:
		return uint32(binary.BigEndian.Uint16(p))
	case 4:
		return binary.BigEndian.Uint32(p)
	case 8:
		return uint32(binary.BigEndian.Uint64(p))
	default:
		v := uint32(0)
		for _, b := range p {
			v = v<<8 | uint32(b)
		}
		return v
	}
}

func mul64u(a, b uint64) (uint64, uint64) { return bits.Mul64(a, b) }
