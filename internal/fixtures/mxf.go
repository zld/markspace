package fixtures

// MXF fixture layout
//
// The generated file is a deliberately small operational-pattern MXF: a
// 14-byte partition pack, a header metadata KLV carrying a local tag set,
// then a content package of essence KLVs. All identifiers below are local
// constants; no external registers or assets are consulted.
//
//   partition pack  (key 060e2b34020501010d0102010102xx)  length 88
//   header KLV      (key 060e2b34020501010d0102010102ff)  local tag set
//   essence KLV x N (key 060e2b34010201010d0103010200NN)  payload only
//
// The header set uses the SMPTE universal label prefix plus a local tag
// encoding that mirrors the subset of MXF metadata this tool understands:
//
//   tag 0x3001  track id              u32
//   tag 0x3002  track kind            u32 (1 video, 2 audio)
//   tag 0x3003  edit rate numerator   u32
//   tag 0x3004  edit rate denominator u32
//   tag 0x3005  sample count          u32
//   tag 0x3006  rotation degrees      u32
//   tag 0x3007  colour primaries      u16
//   tag 0x3008  transfer char.        u16
//   tag 0x3009  matrix coeff.         u16
//   tag 0x300a  full range flag       u8
//   tag 0x300b  crop left             u16
//   tag 0x300c  crop top              u16
//   tag 0x300d  crop right            u16
//   tag 0x300e  crop bottom           u16

import (
	"encoding/binary"
	"fmt"
)

// MXFSample is one essence item in a generated MXF.
type MXFSample struct {
	TrackID  uint32
	Number   int
	DTS      int64 // edit units
	Offset   int64 // absolute byte offset of essence payload
	Size     uint32
	Keyframe bool
	Payload  []byte
}

// MXFFile is a generated MXF and the facts needed to verify parsing.
type MXFFile struct {
	Bytes        []byte
	Samples      []MXFSample
	EssenceStart int64
}

// MXFSpec controls the generated content package.
type MXFSpec struct {
	VideoSamples int
	AudioSamples int
	Rotation     int
	Color        *Color
	Crop         *Crop
}

// DefaultMXFSpec returns a two-track 16-sample MXF whose descriptors differ
// from the default MP4 so conflict handling has something to resolve.
func DefaultMXFSpec() MXFSpec {
	return MXFSpec{
		VideoSamples: VideoSamples,
		AudioSamples: AudioSamples,
		Rotation:     180,
		Color:        &Color{Primaries: 1, Transfer: 1, Matrix: 1},
	}
}

var mxfPartitionKey = []byte{
	0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x01, 0x02, 0x01, 0x01, 0x02, 0x04, 0x00,
}

var mxfHeaderKey = []byte{
	0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x01, 0x02, 0x01, 0x01, 0xff, 0x00, 0x00,
}

func mxfEssenceKey(track uint8) []byte {
	return []byte{
		0x06, 0x0e, 0x2b, 0x34, 0x01, 0x02, 0x01, 0x01, 0x0d, 0x01, 0x03, 0x01, 0x02, 0x00, 0x00, track,
	}
}

// klv encodes a 16-byte key and payload with a BER length.
func klv(key, payload []byte) []byte {
	out := append([]byte(nil), key...)
	out = append(out, berLen(len(payload))...)
	return append(out, payload...)
}

func berLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	if n <= 0xff {
		return []byte{0x81, byte(n)}
	}
	if n <= 0xffff {
		return []byte{0x82, byte(n >> 8), byte(n)}
	}
	return []byte{0x83, byte(n >> 16), byte(n >> 8), byte(n)}
}

// localTagSet renders the header metadata as local tag pairs with 4-byte
// lengths, as used by the minimal profile this tool targets.
type localTagSet struct{ buf []byte }

func (s *localTagSet) u32(tag uint16, v uint32) {
	s.buf = append(s.buf, byte(tag>>8), byte(tag))
	s.buf = append(s.buf, u32(v)...)
}

func (s *localTagSet) u16(tag uint16, v uint16) {
	s.buf = append(s.buf, byte(tag>>8), byte(tag))
	s.buf = append(s.buf, u16(v)...)
}

func (s *localTagSet) u8(tag uint16, v uint8) {
	s.buf = append(s.buf, byte(tag>>8), byte(tag), v)
}

// GenerateMXF builds the fixture described at the top of this file.
func GenerateMXF(spec MXFSpec) (*MXFFile, error) {
	if spec.VideoSamples <= 0 || spec.AudioSamples <= 0 {
		return nil, fmt.Errorf("mxf fixture needs both tracks")
	}
	type essence struct {
		track uint8
		data  []byte
		key   bool
	}
	var items []essence
	var samples []MXFSample
	for i := 1; i <= spec.VideoSamples; i++ {
		size := 64 + (i*7)%23
		p := make([]byte, size)
		copy(p, formatTag(1, i))
		items = append(items, essence{track: 1, data: p, key: (i-1)%8 == 0})
	}
	for i := 1; i <= spec.AudioSamples; i++ {
		size := 32 + (i*5)%11
		p := make([]byte, size)
		copy(p, formatTag(1, 100+i))
		items = append(items, essence{track: 2, data: p})
	}

	video := &localTagSet{}
	video.u32(0x3001, 1)
	video.u32(0x3002, 1)
	video.u32(0x3003, VideoSampleDur)
	video.u32(0x3004, VideoTimescale)
	video.u32(0x3005, uint32(spec.VideoSamples))
	video.u32(0x3006, uint32(spec.Rotation))
	if spec.Color != nil {
		video.u32(0x3007, uint32(spec.Color.Primaries)|uint32(spec.Color.Transfer)<<16)
		video.u16(0x3009, spec.Color.Matrix)
		var full uint8
		if spec.Color.FullRange {
			full = 1
		}
		video.u8(0x300a, full)
	}
	if spec.Crop != nil {
		video.u16(0x300b, uint16(spec.Crop.Left))
		video.u16(0x300c, uint16(spec.Crop.Top))
		video.u16(0x300d, uint16(spec.Crop.Right))
		video.u16(0x300e, uint16(spec.Crop.Bottom))
	}
	track2 := &localTagSet{}
	track2.u32(0x3001, 2)
	track2.u32(0x3002, 2)
	track2.u32(0x3003, AudioSampleDur)
	track2.u32(0x3004, AudioTimescale)
	track2.u32(0x3005, uint32(spec.AudioSamples))

	headerData := append([]byte(nil), video.buf...)
	headerData = append(headerData, track2.buf...)

	// Partition payload: major/minor version and zeroed body/sink offsets,
	// which this profile does not interpret.
	partitionPayload := append([]byte{0x00, 0x01, 0x00, 0x00}, make([]byte, 64)...)
	out := append([]byte(nil), klv(mxfPartitionKey, partitionPayload)...)
	out = append(out, klv(mxfHeaderKey, headerData)...)
	essenceStart := int64(len(out))
	// First pass: compute sizes so absolute offsets are known, then write.
	for i, it := range items {
		off := int64(len(out)) + 16 + int64(len(berLen(len(it.data))))
		samples = append(samples, MXFSample{
			TrackID: uint32(it.track), Number: i + 1,
			Offset: off, Size: uint32(len(it.data)), Keyframe: it.key, Payload: it.data,
		})
		out = append(out, klv(mxfEssenceKey(it.track), it.data)...)
	}
	// Fill decode times: separate counters per track.
	v, a := int64(0), int64(0)
	for i := range samples {
		if samples[i].TrackID == 1 {
			samples[i].DTS = v
			v += VideoSampleDur
		} else {
			samples[i].DTS = a
			a += AudioSampleDur
		}
	}
	return &MXFFile{Bytes: out, Samples: samples, EssenceStart: essenceStart}, nil
}

// CorruptMXFAt returns a copy with the essence length field of the first
// essence KLV overwritten to claim more bytes than the file holds.
func CorruptMXFAt(data []byte, essenceStart int64) []byte {
	out := append([]byte(nil), data...)
	if essenceStart+16 >= int64(len(out)) {
		return out
	}
	// Overwrite the first essence KLV's BER length with a huge 3-byte value.
	pos := int(essenceStart) + 16
	if pos+3 <= len(out) {
		out[pos] = 0x83
		binary.BigEndian.PutUint16(out[pos+1:pos+3], 0x7fff)
	}
	return out
}
