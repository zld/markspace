// Package sample generates deterministic synthetic media fixtures in
// code. No external files are referenced; the same seed always produces
// the same bytes, which is what makes the demo's output digest stable.
package sample

import (
	"math"
	"math/rand"

	"mediacore/ingest"
)

// H264 builds a synthetic Annex B H.264 stream: SPS, PPS, then gops
// groups of one IDR followed by gop-1 non-IDR slices. Payload bytes are
// seeded noise. The returned index's offsets are payload offsets (after
// the start code), matching ingest.Unit.Offset.
func H264(seed int64, gops, gop int) ([]byte, ingest.KeyframeIndex) {
	r := rand.New(rand.NewSource(seed))
	var buf []byte
	put := func(hdr byte, n int) {
		buf = append(buf, 0, 0, 0, 1, hdr)
		for i := 0; i < n; i++ {
			buf = append(buf, byte(r.Intn(256)))
		}
	}
	put(0x67, 8) // SPS
	put(0x68, 4) // PPS
	var idx ingest.KeyframeIndex
	for g := 0; g < gops; g++ {
		idx.Offsets = append(idx.Offsets, int64(len(buf)+4))
		payload := 64 + r.Intn(128)
		idx.Sizes = append(idx.Sizes, int64(payload+1))
		put(0x65, payload) // IDR
		for k := 1; k < gop; k++ {
			put(0x41, 32+r.Intn(64)) // non-IDR slice
		}
	}
	idx.Digest = ingest.DigestOf(buf)
	return buf, idx
}

// H265 builds a synthetic Annex B H.265 stream (VPS/SPS/PPS then IDR /
// trailing units) with its keyframe index.
func H265(seed int64, gops, gop int) ([]byte, ingest.KeyframeIndex) {
	r := rand.New(rand.NewSource(seed))
	var buf []byte
	put := func(hdr0, hdr1 byte, n int) {
		buf = append(buf, 0, 0, 0, 1, hdr0, hdr1)
		for i := 0; i < n; i++ {
			buf = append(buf, byte(r.Intn(256)))
		}
	}
	put(0x40, 0x01, 8)  // VPS
	put(0x42, 0x01, 10) // SPS
	put(0x44, 0x01, 4)  // PPS
	var idx ingest.KeyframeIndex
	for g := 0; g < gops; g++ {
		idx.Offsets = append(idx.Offsets, int64(len(buf)+4))
		payload := 64 + r.Intn(128)
		idx.Sizes = append(idx.Sizes, int64(payload+2))
		put(0x26, 0x01, payload) // IDR_W_RADL (type 19)
		for k := 1; k < gop; k++ {
			put(0x02, 0x01, 32+r.Intn(64)) // TRAIL_R (type 1)
		}
	}
	idx.Digest = ingest.DigestOf(buf)
	return buf, idx
}

// AV1 builds a synthetic low-overhead OBU stream: sequence header, then
// per GOP a key frame OBU followed by inter frame OBUs. Frame OBU first
// payload byte: show_existing_frame=0, frame_type in bits 6..5.
func AV1(seed int64, gops, gop int) ([]byte, ingest.KeyframeIndex) {
	r := rand.New(rand.NewSource(seed))
	var buf []byte
	put := func(obuType byte, firstPayload byte, n int) {
		payload := make([]byte, n)
		payload[0] = firstPayload
		for i := 1; i < n; i++ {
			payload[i] = byte(r.Intn(256))
		}
		hdr := (obuType << 3) | 0x02 // has_size_field
		buf = append(buf, hdr, byte(len(payload))) // sizes stay < 128
		buf = append(buf, payload...)
	}
	put(1, 0, 6) // sequence header
	var idx ingest.KeyframeIndex
	for g := 0; g < gops; g++ {
		idx.Offsets = append(idx.Offsets, int64(len(buf)+2))
		n := 1 + 32 + r.Intn(64)
		idx.Sizes = append(idx.Sizes, int64(n))
		put(6, 0x00, n) // frame OBU, frame_type=KEY
		for k := 1; k < gop; k++ {
			put(6, 0x20, 1+16+r.Intn(32)) // frame_type=INTER (0b01 << 5)
		}
	}
	idx.Digest = ingest.DigestOf(buf)
	return buf, idx
}

// Audio renders a deterministic interleaved PCM signal: 440 Hz + 880 Hz
// sines at 0.5 amplitude plus a whisper of seeded noise, so loudness and
// resampling have something real to chew on.
func Audio(seed int64, rate, channels, durationMS int) []float32 {
	r := rand.New(rand.NewSource(seed))
	frames := rate * durationMS / 1000
	out := make([]float32, frames*channels)
	for f := 0; f < frames; f++ {
		t := float64(f) / float64(rate)
		base := 0.5*math.Sin(2*math.Pi*440*t) + 0.2*math.Sin(2*math.Pi*880*t)
		for c := 0; c < channels; c++ {
			phase := 1.0 + 0.05*float64(c)
			out[f*channels+c] = float32(base*phase) + 0.001*r.Float32()
		}
	}
	return out
}
