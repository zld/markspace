package manifest

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"

	"mediacore/internal/audio"
)

// Payload framing. Each RTP packet carries a small header followed by
// quantized mono-mixed PCM, so the manifest's byte accounting describes real
// payload rather than synthetic padding.
//
//	frameCount u16 | sampleRate u16/100 | channels u8 | reserved u8
const payloadHeader = 6

// payloadBytes returns the exact byte budget for one packet at a target
// bitrate. Rounding is fixed: nearest byte, ties up.
func payloadBytes(cfg Config, frames int) (int, error) {
	if cfg.TargetKbps <= 0 || cfg.SampleRate <= 0 || frames <= 0 {
		return 0, fmt.Errorf("manifest: invalid payload budget inputs")
	}
	bitsPerSecond := int64(cfg.TargetKbps) * 1000
	samplesPerPacket := int64(frames)
	bits := bitsPerSecond * samplesPerPacket / int64(cfg.SampleRate)
	if bits < 0 {
		return 0, fmt.Errorf("manifest: negative payload budget")
	}
	// Round to the nearest byte with ties up, without float64.
	bytes := (bits + 4) / 8
	if bytes < payloadHeader+1 {
		bytes = payloadHeader + 1
	}
	return int(bytes), nil
}

// Shape encodes a buffer as an RTP payload of exactly the target size. PCM is
// first quantized to 8-bit signed, then scaled and padded with a deterministic
// low-level dither pattern when the budget is larger than the audio needs.
// Padding never modifies the audio: it is appended after the valid sample
// run and is marked by the frame count in the header.
func Shape(b *audio.Buffer, targetKbps, rate int) ([]byte, error) {
	if b == nil {
		return nil, fmt.Errorf("manifest: nil buffer")
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	if rate <= 0 {
		rate = 48000
	}
	cfg := Config{TargetKbps: targetKbps, SampleRate: rate}
	budget, err := payloadBytes(cfg, b.Frames)
	if err != nil {
		return nil, err
	}

	// Mono-mix for the wire so channel count does not change payload size.
	ch := b.Layout.NumChannels()
	q := make([]int8, b.Frames)
	for i := 0; i < b.Frames; i++ {
		var acc float64
		for c := 0; c < ch; c++ {
			acc += b.Data[i*ch+c]
		}
		acc /= float64(ch)
		v := acc * 127.0
		if v > 127 {
			v = 127
		} else if v < -128 {
			v = -128
		}
		q[i] = int8(math.Round(v))
	}

	out := make([]byte, budget)
	binary.BigEndian.PutUint16(out[0:], uint16(minInt(b.Frames, 0xFFFF)))
	binary.BigEndian.PutUint16(out[2:], uint16(minInt(rate/100, 0xFFFF)))
	out[4] = byte(ch)
	out[5] = 0
	body := out[payloadHeader:]
	if len(q) < len(body) {
		copy(body, signedBytes(q))
		// Deterministic triangular dither, seeded by position only.
		for i := len(q); i < len(body); i++ {
			body[i] = byte(int(uint32(i)*2654435761>>24) & 0x03)
		}
	} else {
		// Budget too small for all samples: take an evenly spaced prefix so
		// the packet still represents the span rather than a truncated start.
		for i := range body {
			src := i * len(q) / len(body)
			body[i] = byte(q[src])
		}
	}
	return out, nil
}

func signedBytes(q []int8) []byte {
	out := make([]byte, len(q))
	for i, v := range q {
		out[i] = byte(v)
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// marshalStable encodes JSON without HTML escaping and without map ordering
// nondeterminism (the manifest has no maps).
func marshalStable(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Marshal renders the manifest for writing to disk.
func Marshal(m *Manifest) ([]byte, error) { return marshalStable(m) }
