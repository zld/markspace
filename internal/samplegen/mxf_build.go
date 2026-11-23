package samplegen

import (
	"bytes"
	"encoding/binary"
)

// MXF local-set tag identifiers understood by the supported subset. They are
// deliberately simple 2-byte tags rather than full SMPTE ULs.
const (
	MXFTagTrackID    uint16 = 0x0101
	MXFTagEditRate   uint16 = 0x0201 // two uint32: numerator, denominator
	MXFTagWidth      uint16 = 0x0301
	MXFTagHeight     uint16 = 0x0302
	MXFTagRotation   uint16 = 0x0401 // one uint16: quarter turns 0..3
	MXFTagCrop       uint16 = 0x0402 // four uint32: x,y,width,height
	MXFTagColour     uint16 = 0x0501 // three uint16 + one byte: cp,tc,mm,fullrange
	MXFTagDescriptor uint16 = 0x0601 // local-set key marker
)

// mxfLocalSetKey is the 16-byte SMPTE-style key prefix marking a local set.
var mxfLocalSetKey = [16]byte{
	0x06, 0x0E, 0x2B, 0x34, 0x01, 0x02, 0x01, 0x01,
	0x0D, 0x01, 0x03, 0x01, 0x00, 0x00, 0x00, 0x00,
}

// MXFLocalSetKey exposes the local-set key so the MXF parser can recognise
// the supported subset without redefining the constant.
func MXFLocalSetKey() [16]byte { return mxfLocalSetKey }

// berLen encodes a length using ASN.1 BER definite form.
func berLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var buf [8]byte
	i := len(buf)
	v := n
	for v > 0 {
		i--
		buf[i] = byte(v & 0xFF)
		v >>= 8
	}
	return append([]byte{0x80 | byte(len(buf)-i)}, buf[i:]...)
}

// klv wraps key and value with a BER length prefix.
func klv(key [16]byte, value []byte) []byte {
	var b bytes.Buffer
	b.Write(key[:])
	b.Write(berLen(len(value)))
	b.Write(value)
	return b.Bytes()
}

func tagItem(tag uint16, value []byte) []byte {
	var b bytes.Buffer
	b.Write(u16BE(tag))
	b.Write(berLen(len(value)))
	b.Write(value)
	return b.Bytes()
}

func u16BE(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func u32BE(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// trackSet serialises one MXF track descriptor as a local-set KLV item.
func trackSet(t MXFTrackSpec) []byte {
	var v bytes.Buffer
	v.Write(tagItem(MXFTagTrackID, u32BE(t.TrackID)))
	var rate bytes.Buffer
	rate.Write(u32BE(t.EditRateN))
	rate.Write(u32BE(t.EditRateD))
	v.Write(tagItem(MXFTagEditRate, rate.Bytes()))
	v.Write(tagItem(MXFTagWidth, u32BE(t.Width)))
	v.Write(tagItem(MXFTagHeight, u32BE(t.Height)))
	v.Write(tagItem(MXFTagRotation, u16BE(uint16(t.Rotation90%4))))
	if t.CropW > 0 && t.CropH > 0 {
		var crop bytes.Buffer
		crop.Write(u32BE(t.CropX))
		crop.Write(u32BE(t.CropY))
		crop.Write(u32BE(t.CropW))
		crop.Write(u32BE(t.CropH))
		v.Write(tagItem(MXFTagCrop, crop.Bytes()))
	}
	var colour bytes.Buffer
	colour.Write(u16BE(t.Primaries))
	colour.Write(u16BE(t.Transfer))
	colour.Write(u16BE(t.Matrix))
	if t.FullRange {
		colour.WriteByte(1)
	} else {
		colour.WriteByte(0)
	}
	v.Write(tagItem(MXFTagColour, colour.Bytes()))
	return klv(mxfLocalSetKey, v.Bytes())
}

// BuildMXF serialises the spec into a length-delimited KLV stream. With
// Truncate set the final KLV item's value is cut mid-way so a parser must
// report a recoverable truncated-item error.
func BuildMXF(spec MXFSpec) []byte {
	var out bytes.Buffer
	for i, t := range spec.Tracks {
		item := trackSet(t)
		if spec.Truncate && i == len(spec.Tracks)-1 {
			item = item[:len(item)*3/5] // keep key + partial length/value
		}
		out.Write(item)
	}
	return out.Bytes()
}
