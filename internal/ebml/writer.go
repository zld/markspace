package ebml

import (
	"bytes"
	"encoding/binary"
)

// EncodeVINT renders an EBML variable-length integer of the requested width.
// Width 0 selects the smallest width that can hold value.
func EncodeVINT(value uint64, width int) []byte {
	if width == 0 {
		for w := 1; w <= 8; w++ {
			// w-byte VINT stores 7w-1 usable bits plus one marker bit.
			maxVal := uint64(1)<<uint(7*w-1) - 1
			if value <= maxVal {
				width = w
				break
			}
		}
		if width == 0 {
			width = 8
		}
	}
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		out[i] = byte(value & 0xFF)
		value >>= 8
	}
	// The top available bits hold data; clear the marker slot, then set it.
	out[0] &^= 0x80 >> uint(width-1)
	out[0] |= 0x80 >> uint(width-1)
	return out
}

// EncodeUnknownSize returns the all-ones unknown-length marker for a width.
func EncodeUnknownSize(width int) []byte {
	out := make([]byte, width)
	for i := range out {
		out[i] = 0xFF
	}
	return out
}

// ElementBytes wraps a payload in ID + size headers.
func ElementBytes(id, payload []byte) []byte {
	return ElementBytesSized(id, payload, false)
}

// ElementBytesSized optionally emits an unknown-length size field.
func ElementBytesSized(id, payload []byte, unknownSize bool) []byte {
	var size []byte
	if unknownSize {
		size = EncodeUnknownSize(4)
	} else {
		size = EncodeVINT(uint64(len(payload)), 0)
	}
	out := make([]byte, 0, len(id)+len(size)+len(payload))
	out = append(out, id...)
	out = append(out, size...)
	out = append(out, payload...)
	return out
}

// UintElement encodes an unsigned integer EBML element.
func UintElement(id []byte, value uint64) []byte {
	var raw []byte
	if value == 0 {
		raw = []byte{0}
	} else {
		v := value
		for v > 0 {
			raw = append([]byte{byte(v & 0xFF)}, raw...)
			v >>= 8
		}
	}
	return ElementBytes(id, raw)
}

// SimpleBlock builds a Matroska SimpleBlock: track VINT, int16 relative
// timecode, flags byte, then the opaque codec payload.
func SimpleBlock(track uint32, relTC int16, keyframe bool, payload []byte) []byte {
	var buf bytes.Buffer
	buf.Write(EncodeVINT(uint64(track), 0))
	tc := make([]byte, 2)
	binary.BigEndian.PutUint16(tc, uint16(relTC))
	buf.Write(tc)
	var flags byte
	if keyframe {
		flags = 0x80
	}
	buf.WriteByte(flags)
	buf.Write(payload)
	return ElementBytes(IDSimpleBlk, buf.Bytes())
}

// Cluster wraps a timecode element and pre-encoded block elements.
func Cluster(timecode uint64, blocks ...[]byte) []byte {
	var payload bytes.Buffer
	payload.Write(UintElement(IDTimecode, timecode))
	for _, b := range blocks {
		payload.Write(b)
	}
	return ElementBytes(IDCluster, payload.Bytes())
}

// ClusterUnknown wraps blocks with an unknown-length Cluster header.
func ClusterUnknown(timecode uint64, blocks ...[]byte) []byte {
	var payload bytes.Buffer
	payload.Write(UintElement(IDTimecode, timecode))
	for _, b := range blocks {
		payload.Write(b)
	}
	return ElementBytesSized(IDCluster, payload.Bytes(), true)
}

// Segment wraps pre-encoded clusters, using a known or unknown size.
func Segment(body []byte, unknownSize bool) []byte {
	return ElementBytesSized(IDSegment, body, unknownSize)
}

// MinimalHeader returns a fixed EBML header declaring Matroska doctype.
func MinimalHeader() []byte {
	var payload bytes.Buffer
	payload.Write(UintElement([]byte{0x42, 0x86}, 1))                   // EBMLVersion
	payload.Write(UintElement([]byte{0x42, 0xF7}, 1))                   // EBMLReadVersion
	payload.Write(UintElement([]byte{0x42, 0xF2}, 4))                   // MaxIDLength
	payload.Write(UintElement([]byte{0x42, 0xF3}, 8))                   // MaxSizeLength
	payload.Write(ElementBytes([]byte{0x42, 0x82}, []byte("matroska"))) // DocType
	payload.Write(UintElement([]byte{0x42, 0x87}, 4))                   // DocTypeVersion
	payload.Write(UintElement([]byte{0x42, 0x85}, 2))                   // DocTypeReadVersion
	return ElementBytes(IDEBML, payload.Bytes())
}

// BuildMatroska assembles a complete file from a header and cluster bodies.
func BuildMatroska(clusters [][]byte, unknownSegment bool) []byte {
	var body bytes.Buffer
	for _, c := range clusters {
		body.Write(c)
	}
	var out bytes.Buffer
	out.Write(MinimalHeader())
	out.Write(Segment(body.Bytes(), unknownSegment))
	return out.Bytes()
}
