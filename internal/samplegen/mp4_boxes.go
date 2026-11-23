package samplegen

import (
	"bytes"
	"encoding/binary"
	"io"
)

// rotationMatrix returns the 16.16 fixed-point display matrix for the given
// clockwise rotation quarter-turn count.
func rotationMatrix(quarters int) [9]int32 {
	one := int32(0x00010000)
	switch quarters % 4 {
	case 1: // 90 degrees clockwise
		return [9]int32{0, one, 0, -one, 0, 0, 0, 0, 0x40000000}
	case 2: // 180
		return [9]int32{-one, 0, 0, 0, -one, 0, 0, 0, 0x40000000}
	case 3: // 270 clockwise
		return [9]int32{0, -one, 0, one, 0, 0, 0, 0, 0x40000000}
	default:
		return [9]int32{one, 0, 0, 0, one, 0, 0, 0, 0x40000000}
	}
}

// boxWriter wraps an io.Writer and prefixes payloads with an MP4 box header.
type boxWriter struct{ w io.Writer }

func (b boxWriter) box(typ string, payload []byte) error {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(payload)))
	copy(hdr[4:], typ)
	if _, err := b.w.Write(hdr); err != nil {
		return err
	}
	_, err := b.w.Write(payload)
	return err
}

func boxBytes(typ string, payload []byte) []byte {
	var b bytes.Buffer
	boxWriter{&b}.box(typ, payload)
	return b.Bytes()
}

// fullBox prefixes version/flags (version 0, flags zero) onto a payload.
func fullBox(payload []byte) []byte { return append([]byte{0, 0, 0, 0}, payload...) }

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

// crp0Payload encodes the custom crop box: x,y,width,height as four uint32.
func crp0Payload(s MP4TrackSpec) []byte {
	p := make([]byte, 16)
	binary.BigEndian.PutUint32(p[0:], s.CropX)
	binary.BigEndian.PutUint32(p[4:], s.CropY)
	binary.BigEndian.PutUint32(p[8:], s.CropW)
	binary.BigEndian.PutUint32(p[12:], s.CropH)
	return p
}

// colrPayload encodes an nclx colour box by hand so full-range round-trips.
func colrPayload(s MP4TrackSpec) []byte {
	var b bytes.Buffer
	b.Write(s.ColourType[:])
	b.Write(u16(s.Primaries))
	b.Write(u16(s.Transfer))
	b.Write(u16(s.Matrix))
	flag := byte(0)
	if s.FullRange {
		flag = 0x80
	}
	b.WriteByte(flag) // full-range flag + 7 reserved zero bits
	return b.Bytes()
}

func hasColour(s MP4TrackSpec) bool {
	return s.Primaries != 0 || s.Transfer != 0 || s.Matrix != 0
}

// visualEntryPayload builds a minimal avc1 visual sample entry (78 bytes),
// optionally carrying a colr child box.
func visualEntryPayload(s MP4TrackSpec) []byte {
	var b bytes.Buffer
	b.Write(make([]byte, 6))  // reserved
	b.Write(u16(1))           // data_reference_index
	b.Write(u16(0))           // pre_defined
	b.Write(u16(0))           // reserved
	b.Write(make([]byte, 12)) // pre_defined[3]
	b.Write(u16(uint16(s.Width)))
	b.Write(u16(uint16(s.Height)))
	b.Write(u32(0x00480000))  // horiz resolution 72 dpi
	b.Write(u32(0x00480000))  // vert resolution
	b.Write(u32(0))           // reserved
	b.Write(u16(1))           // frame count
	b.Write(make([]byte, 32)) // compressor name
	b.Write(u16(0x0018))      // depth
	b.Write(u16(0xFFFF))      // pre_defined (-1)
	if hasColour(s) {
		b.Write(boxBytes("colr", colrPayload(s)))
	}
	return b.Bytes()
}

func syncSamples(n uint32) []uint32 {
	keys := []uint32{1}
	for k := uint32(11); k <= n; k += 10 {
		keys = append(keys, k)
	}
	return keys
}

// stblPayload builds the sample-table boxes for one track. The stco chunk
// offset field holds stcoMarker and is patched after the layout is measured.
func stblPayload(s MP4TrackSpec) ([]byte, error) {
	var stbl bytes.Buffer
	bw := boxWriter{&stbl}

	var stsd bytes.Buffer
	stsd.Write(fullBox(u32(1)))
	stsd.Write(boxBytes("avc1", visualEntryPayload(s)))
	if err := bw.box("stsd", stsd.Bytes()); err != nil {
		return nil, err
	}

	var stts bytes.Buffer
	stts.Write(fullBox(append(u32(1), append(u32(s.SampleN), u32(s.SampleDur)...)...)))
	if err := bw.box("stts", stts.Bytes()); err != nil {
		return nil, err
	}

	keys := syncSamples(s.SampleN)
	var stss bytes.Buffer
	stss.Write(fullBox(u32(uint32(len(keys)))))
	for _, k := range keys {
		stss.Write(u32(k))
	}
	if err := bw.box("stss", stss.Bytes()); err != nil {
		return nil, err
	}

	var stsz bytes.Buffer
	stsz.Write(fullBox(append(u32(64), u32(s.SampleN)...))) // uniform 64B samples
	if err := bw.box("stsz", stsz.Bytes()); err != nil {
		return nil, err
	}

	var stsc bytes.Buffer
	stsc.Write(fullBox(append(u32(1), append(u32(1), append(u32(s.SampleN), u32(1)...)...)...)))
	if err := bw.box("stsc", stsc.Bytes()); err != nil {
		return nil, err
	}

	// stco: placeholder offset marked with a magic value; after assembly the
	// marker positions are located by search and patched with real offsets.
	var stco bytes.Buffer
	stco.Write(fullBox(u32(1)))
	stco.Write(stcoMarker)
	if err := bw.box("stco", stco.Bytes()); err != nil {
		return nil, err
	}

	if s.CropW > 0 && s.CropH > 0 {
		if err := bw.box("crp0", crp0Payload(s)); err != nil {
			return nil, err
		}
	}
	return stbl.Bytes(), nil
}

// stcoMarker is a distinctive 4-byte pattern placed in every stco offset
// field so the final layout can locate and patch them without nested-offset
// arithmetic. It cannot appear elsewhere in the deterministic fixtures.
var stcoMarker = []byte{0xDE, 0xAD, 0xBE, 0xEF}
