// Package sample deterministically generates the project's own test
// assets: bare H.264/H.265/AV1 streams, decodable frames and an audio
// tone. No external files are referenced. In every generated stream a
// unit header sits at byte offset 37 so corruption demos can point at
// an exact input position.
package sample

import (
	"math"

	"mediacheck/internal/clock"
	"mediacheck/internal/codec"
	"mediacheck/internal/fault"
	"mediacheck/internal/index"
)

// HeaderOffset is the byte offset of a unit header in every generated
// stream, used by the corruption demo.
const HeaderOffset = 37

// Asset is one generated source stream with derived metadata.
type Asset struct {
	Kind   codec.Kind
	Stream []byte
	Units  []codec.Unit
	Index  index.Index
	Ticks  []clock.Ticks // per-unit presentation ticks on the 90kHz line
}

// FrameTicks is the presentation interval: 3000 ticks at 90kHz = 30fps.
const FrameTicks = 3000

// Generate builds the asset for one codec.
func Generate(k codec.Kind) (*Asset, error) {
	var data []byte
	switch k {
	case codec.H264:
		data = h264Stream()
	case codec.H265:
		data = h265Stream()
	case codec.AV1:
		data = av1Stream()
	default:
		return nil, fault.New(fault.ErrBadHeader, fault.NoOffset, "unknown codec kind %d", k)
	}
	units, err := codec.Units(data, k)
	if err != nil {
		return nil, err
	}
	ticks := make([]clock.Ticks, len(units))
	frame := 0
	for i, u := range units {
		ticks[i] = clock.Ticks(frame * FrameTicks)
		if IsPicture(u) {
			frame++
		}
	}
	return &Asset{Kind: k, Stream: data, Units: units, Index: index.Build(units), Ticks: ticks}, nil
}

func IsPicture(u codec.Unit) bool {
	switch u.Name {
	case "slice", "IDR", "trail", "frame":
		return true
	}
	return false
}

// CorruptAt returns a copy of data with the forbidden header bit set
// at off, simulating a damaged input.
func CorruptAt(data []byte, off int64) []byte {
	out := append([]byte(nil), data...)
	out[off] |= 0x80
	return out
}

// annexBUnit appends a 4-byte-start-code unit with a deterministic
// payload of the given size.
func annexBUnit(hdr []byte, payload int, seed byte) []byte {
	b := []byte{0, 0, 0, 1}
	b = append(b, hdr...)
	for j := 0; j < payload; j++ {
		b = append(b, seed+byte(j)*7)
	}
	return b
}

func h264Stream() []byte {
	var d []byte
	d = append(d, annexBUnit([]byte{0x0C}, 28, 0xF0)...) // filler; next header at 37
	d = append(d, annexBUnit([]byte{0x67}, 10, 0x10)...) // SPS @37
	d = append(d, annexBUnit([]byte{0x68}, 6, 0x20)...)  // PPS
	for g := 0; g < 3; g++ {
		d = append(d, annexBUnit([]byte{0x65}, 40, byte(0x30+g*3))...) // IDR
		d = append(d, annexBUnit([]byte{0x41}, 32, byte(0x31+g*3))...) // slice
		d = append(d, annexBUnit([]byte{0x41}, 32, byte(0x32+g*3))...) // slice
	}
	return d
}

func h265Stream() []byte {
	var d []byte
	d = append(d, annexBUnit([]byte{0x4C, 0x01}, 27, 0xF0)...) // filler; next header at 37
	d = append(d, annexBUnit([]byte{0x40, 0x01}, 8, 0x10)...)  // VPS @37
	d = append(d, annexBUnit([]byte{0x42, 0x01}, 10, 0x20)...) // SPS
	d = append(d, annexBUnit([]byte{0x44, 0x01}, 6, 0x30)...)  // PPS
	for g := 0; g < 3; g++ {
		d = append(d, annexBUnit([]byte{0x26, 0x01}, 40, byte(0x40+g*3))...) // IDR
		d = append(d, annexBUnit([]byte{0x02, 0x01}, 32, byte(0x41+g*3))...) // trail
		d = append(d, annexBUnit([]byte{0x02, 0x01}, 32, byte(0x42+g*3))...) // trail
	}
	return d
}

// obu appends one low-overhead OBU (size < 128, single-byte leb128).
func obu(hdr byte, payload []byte) []byte {
	return append([]byte{hdr, byte(len(payload))}, payload...)
}

func av1Stream() []byte {
	var d []byte
	d = append(d, obu(0x7A, make([]byte, 35))...) // padding; next header at 37
	d = append(d, obu(0x0A, []byte{0x11, 0x22, 0x33, 0x44})...) // seq_header @37
	fill := func(seed byte, n int) []byte {
		p := make([]byte, n)
		for j := range p {
			p[j] = seed + byte(j)*7
		}
		return p
	}
	for g := 0; g < 3; g++ {
		d = append(d, obu(0x12, nil)...) // temporal delimiter
		key := fill(byte(0x50+g*3), 40)
		key[0] = 0x00 // frame_type KEY
		d = append(d, obu(0x32, key)...)
		inter1 := fill(byte(0x51+g*3), 32)
		inter1[0] = 0xC0 // frame_type INTER
		d = append(d, obu(0x32, inter1)...)
		inter2 := fill(byte(0x52+g*3), 32)
		inter2[0] = 0xC0
		d = append(d, obu(0x32, inter2)...)
	}
	return d
}

// DecodeFrames deterministically "decodes" n 64x64 8-bit luma frames
// for the codec. The output is the offscreen rendering used for frame
// hashes and pixel statistics.
func DecodeFrames(k codec.Kind, n int) [][]byte {
	seed := byte(k)*40 + 7
	frames := make([][]byte, n)
	for i := 0; i < n; i++ {
		f := make([]byte, 64*64)
		for y := 0; y < 64; y++ {
			for x := 0; x < 64; x++ {
				f[y*64+x] = byte(x*3 + y*5 + i*11 + int(seed))
			}
		}
		frames[i] = f
	}
	return frames
}

// Tone returns n samples of a deterministic 48kHz sine at 0.5 full
// scale, used for loudness peak measurement.
func Tone(n int) []int16 {
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = int16(math.Sin(float64(i)*2*math.Pi*1000/48000) * 0.5 * 32767)
	}
	return pcm
}
