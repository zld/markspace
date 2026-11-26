// Package samplegen creates deterministic in-repo media fixtures and fault inputs.
package samplegen

import (
	"encoding/binary"
	"math"
)

const (
	Width        = 32
	Height       = 18
	FrameBytes   = Width * Height * 4
	FPS          = 24
	FrameCount   = 48 // two seconds at 24 fps
	AudioRate    = 48000
	AudioChunks  = 100
	ChunkSamples = 960
	AudioSamples = AudioRate * 2
)

// VideoFrame renders a deterministic RGBA8-v1 frame.
func VideoFrame(sequence, frame int) []byte {
	b := make([]byte, FrameBytes)
	p := 0
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			b[p] = byte(x*7 + frame*5 + sequence*41)
			b[p+1] = byte(y*11 + frame*3 + sequence*23)
			b[p+2] = byte((x+y)*5 + frame*7 + sequence*79)
			b[p+3] = 255
			p += 4
		}
	}
	return b
}

// TransitionFrame produces a deterministic crossfade between two RGBA frames.
func TransitionFrame(a, b []byte, numerator, denominator int) []byte {
	out := make([]byte, FrameBytes)
	if denominator <= 0 {
		denominator = 1
	}
	for i := 0; i < FrameBytes; i += 4 {
		for c := 0; c < 3; c++ {
			av := int(a[i+c]) * (denominator - numerator)
			bv := int(b[i+c]) * numerator
			out[i+c] = byte((av + bv + denominator/2) / denominator)
		}
		out[i+3] = 255
	}
	return out
}

// AudioChunk renders one phase-continuous block of signed 16-bit PCM.
func AudioChunk(chunk int) []byte {
	start := chunk * ChunkSamples
	b := make([]byte, ChunkSamples*2)
	for i := 0; i < ChunkSamples; i++ {
		n := start + i
		v := int16(8192.0 * math.Sin(2*math.Pi*440*float64(n)/AudioRate))
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

// AllAudio returns the complete two-second mono PCM stream.
func AllAudio() []int16 {
	out := make([]int16, AudioSamples)
	for c := 0; c < AudioSamples/ChunkSamples; c++ {
		raw := AudioChunk(c)
		for i := 0; i < ChunkSamples; i++ {
			out[c*ChunkSamples+i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
		}
	}
	return out
}
