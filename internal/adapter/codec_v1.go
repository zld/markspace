package adapter

import (
	"encoding/binary"
	"fmt"
	"math"
)

// RGBA8V1 is the only promised lossless video reference codec.
type RGBA8V1 struct{}

func NewRGBA8V1() *RGBA8V1 { return &RGBA8V1{} }
func (c *RGBA8V1) Descriptor() Descriptor {
	return Descriptor{ID: ID{Name: CodecRGBA8, Version: APIVersion}, Kind: "codec", Codec: CodecRGBA8}
}

func (c *RGBA8V1) Validate(t *Track) error {
	if t.Codec.Name != CodecRGBA8 || t.Codec.Version != APIVersion {
		return fmt.Errorf("%w: track %s has %s/v%d", ErrUnknownCodec, t.ID, t.Codec.Name, t.Codec.Version)
	}
	if t.Width <= 0 || t.Height <= 0 {
		return fmt.Errorf("%w: track %s has invalid dimensions %dx%d", ErrBadInput, t.ID, t.Width, t.Height)
	}
	want := t.Width * t.Height * 4
	for _, s := range t.Samples {
		if s.Size != want {
			return fmt.Errorf("%w: track %s sample %d RGBA size=%d want=%d", ErrBadInput, t.ID, s.Index, s.Size, want)
		}
	}
	return nil
}

// DecodeRGBA returns a defensive copy after exact frame-size validation.
func (c *RGBA8V1) DecodeRGBA(s Sample, width, height int) ([]byte, error) {
	want := width * height * 4
	if len(s.Payload) != want || s.Size != want {
		return nil, fmt.Errorf("%w: RGBA sample %d has %d bytes, want %d", ErrBadInput, s.Index, len(s.Payload), want)
	}
	out := make([]byte, want)
	copy(out, s.Payload)
	return out, nil
}

// PCMS16LEV1 is the promised signed 16-bit little-endian audio codec.
type PCMS16LEV1 struct{}

func NewPCMS16LEV1() *PCMS16LEV1 { return &PCMS16LEV1{} }
func (c *PCMS16LEV1) Descriptor() Descriptor {
	return Descriptor{ID: ID{Name: CodecPCMS16, Version: APIVersion}, Kind: "codec", Codec: CodecPCMS16}
}

func (c *PCMS16LEV1) Validate(t *Track) error {
	if t.Codec.Name != CodecPCMS16 || t.Codec.Version != APIVersion {
		return fmt.Errorf("%w: track %s has %s/v%d", ErrUnknownCodec, t.ID, t.Codec.Name, t.Codec.Version)
	}
	if t.SampleRate <= 0 || t.Channels <= 0 {
		return fmt.Errorf("%w: track %s invalid PCM format rate=%d channels=%d", ErrBadInput, t.ID, t.SampleRate, t.Channels)
	}
	frameBytes := 2 * t.Channels
	for _, s := range t.Samples {
		if s.Size <= 0 || s.Size%frameBytes != 0 {
			return fmt.Errorf("%w: track %s sample %d size %d not divisible by %d", ErrBadInput, t.ID, s.Index, s.Size, frameBytes)
		}
	}
	return nil
}

// DecodeS16 interprets payload as interleaved signed 16-bit little-endian.
func (c *PCMS16LEV1) DecodeS16(payload []byte) ([]int16, error) {
	if len(payload)%2 != 0 {
		return nil, fmt.Errorf("%w: PCM byte length %d is odd", ErrBadInput, len(payload))
	}
	out := make([]int16, len(payload)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(payload[2*i:]))
	}
	return out, nil
}

// S16PeakDBFS computes the sample peak. Silence returns negative infinity.
func S16PeakDBFS(samples []int16) float64 {
	var peak float64
	for _, v := range samples {
		a := math.Abs(float64(v))
		if a > peak {
			peak = a
		}
	}
	if peak == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(peak/32768.0)
}
