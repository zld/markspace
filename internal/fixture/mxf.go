package fixture

import (
	"bytes"
	"encoding/binary"
)

// MXF subset item keys. The project MXF subset uses fixed 16-byte SMPTE-style
// labels: bytes [0:8] are a fixed UL prefix and the last byte identifies the
// semantic item. Keys are shared verbatim by writer and parser.
var (
	mxfPartitionPrefix = [8]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x05, 0x01, 0x01}
	mxfItemPrefix      = [8]byte{0x06, 0x0E, 0x2B, 0x34, 0x01, 0x01, 0x01, 0x0A}
)

// mxfItemKey builds a 16-byte item key with the given identifier byte.
func mxfItemKey(id byte) [16]byte {
	var k [16]byte
	copy(k[0:8], mxfItemPrefix[:])
	k[15] = id
	return k
}

const (
	mxfIDTrackStart byte = 0x01
	mxfIDKind       byte = 0x02
	mxfIDTimescale  byte = 0x03
	mxfIDDuration   byte = 0x04
	mxfIDWidth      byte = 0x10
	mxfIDHeight     byte = 0x11
	mxfIDRotation   byte = 0x12
	mxfIDCrop       byte = 0x13
	mxfIDColor      byte = 0x14
	mxfIDChannels   byte = 0x20
	mxfIDRate       byte = 0x21
	mxfIDSampleSize byte = 0x22
	mxfIDSample     byte = 0x30
)

// partitionKey is the fixed Header Partition Pack label.
var partitionKey = func() [16]byte {
	var k [16]byte
	copy(k[0:8], mxfPartitionPrefix[:])
	k[8], k[9], k[10], k[11], k[12], k[13], k[14], k[15] =
		0x0D, 0x01, 0x02, 0x01, 0x01, 0x00, 0x00, 0x00
	return k
}()

// encodeBERLength emits an ASN.1 BER length: short form below 128, long form
// above, matching KLV/SMPTE 336M.
func encodeBERLength(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(n))
	i := 0
	for i < 7 && raw[i] == 0 {
		i++
	}
	out := []byte{0x80 | byte(8-i)}
	return append(out, raw[i:]...)
}

// klv wraps a key and value with a BER length.
func klv(key [16]byte, value []byte) []byte {
	out := make([]byte, 0, 16+9+len(value))
	out = append(out, key[:]...)
	out = append(out, encodeBERLength(len(value))...)
	return append(out, value...)
}

// BuildMXF serializes a Spec into the project-subset MXF byte stream: one
// header partition pack followed by per-track KLV item groups.
func BuildMXF(s Spec) []byte {
	out := bytes.NewBuffer(nil)
	// Header Partition Pack: key + BER length + a small payload.
	out.Write(klv(partitionKey, []byte{0x00, 0x00, 0x00, 0x01}))
	writeMXFVideo(out, s.Video)
	for _, a := range s.Audio {
		writeMXFAudio(out, a)
	}
	return out.Bytes()
}

func writeMXFVideo(out *bytes.Buffer, v VideoSpec) {
	out.Write(klv(mxfItemKey(mxfIDTrackStart), u32(v.TrackID)))
	out.Write(klv(mxfItemKey(mxfIDKind), []byte("vide")))
	out.Write(klv(mxfItemKey(mxfIDTimescale), u32(v.Timescale)))
	out.Write(klv(mxfItemKey(mxfIDDuration), u32(v.Frames*v.Delta)))
	out.Write(klv(mxfItemKey(mxfIDWidth), u16(v.Width)))
	out.Write(klv(mxfItemKey(mxfIDHeight), u16(v.Height)))
	out.Write(klv(mxfItemKey(mxfIDRotation), i32(int32(v.Rotation))))
	if v.Crop != nil {
		c := v.Crop
		crop := bytes.NewBuffer(nil)
		for _, x := range []uint32{c.CleanWidthN, c.CleanWidthD, c.CleanHeightN, c.CleanHeightD} {
			crop.Write(u32(x))
		}
		crop.Write(i32(c.HorizOffN))
		crop.Write(i32(c.HorizOffD))
		crop.Write(i32(c.VertOffN))
		crop.Write(i32(c.VertOffD))
		out.Write(klv(mxfItemKey(mxfIDCrop), crop.Bytes()))
	}
	for _, c := range v.Colors {
		body := bytes.NewBuffer(nil)
		body.Write(rawStr(c.Type))
		body.Write(u16(c.Primaries))
		body.Write(u16(c.Transfer))
		body.Write(u16(c.Matrix))
		flag := byte(0)
		if c.FullRange {
			flag = 1
		}
		body.Write(u8(flag))
		out.Write(klv(mxfItemKey(mxfIDColor), body.Bytes()))
	}
	// One stts-equivalent KLV per frame keeps samples individually addressable.
	for i := uint32(0); i < v.Frames; i++ {
		out.Write(klv(mxfItemKey(mxfIDSample), append(u32(i), u32(v.Delta)...)))
	}
}

func writeMXFAudio(out *bytes.Buffer, a AudioSpec) {
	out.Write(klv(mxfItemKey(mxfIDTrackStart), u32(a.TrackID)))
	out.Write(klv(mxfItemKey(mxfIDKind), []byte("soun")))
	out.Write(klv(mxfItemKey(mxfIDTimescale), u32(a.Timescale)))
	out.Write(klv(mxfItemKey(mxfIDDuration), u32(a.Samples*a.Delta)))
	out.Write(klv(mxfItemKey(mxfIDChannels), u16(a.Channels)))
	out.Write(klv(mxfItemKey(mxfIDRate), u32(a.SampleRate)))
	out.Write(klv(mxfItemKey(mxfIDSampleSize), u16(a.SampleSize)))
	for i := uint32(0); i < a.Samples; i++ {
		out.Write(klv(mxfItemKey(mxfIDSample), append(u32(i), u32(a.Delta)...)))
	}
}
