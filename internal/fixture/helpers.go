package fixture

import (
	"encoding/binary"
	"math/rand"
)

// frameSize derives a deterministic coded-frame byte count from the target
// bitrate. Video has one key frame every ten frames with an I/P size ratio of
// 1.9, balanced so the segment average matches target (P jitter within ±3%).
// Audio has no key frames; it tracks the target directly with mild jitter.
func frameSize(bitrate, framesPerSec int64, key, video bool, rng *rand.Rand) int {
	base := bitrate / 8 / framesPerSec
	if !video {
		jitter := 1.0 + (rng.Float64()-0.5)*0.04
		return int(float64(base) * jitter)
	}
	if key {
		return int(float64(base) * 1.9)
	}
	jitter := 1.0 + (rng.Float64()-0.5)*0.06
	return int(float64(base) * 0.9 * jitter)
}

// boundedQuality returns a deterministic finite quality score in (0,1].
func boundedQuality(rng *rand.Rand) float64 {
	v := 0.82 + rng.Float64()*0.16
	if v > 1 {
		v = 1
	}
	return v
}

// samplesDuration sums sample durations in representation timescale units.
func samplesDuration(ss []Sample) int64 {
	var d int64
	for _, s := range ss {
		d += s.Duration
	}
	return d
}

// sizedPayload builds a deterministic byte payload. Bytes are a function of
// the representation id and segment number, so identical inputs hash equal.
func sizedPayload(repID string, seq, total int) []byte {
	out := make([]byte, total)
	seed := uint32(0x811c9dc5)
	for _, c := range repID {
		seed ^= uint32(c)
		seed *= 16777619
	}
	seed ^= uint32(seq) * 2654435761
	for i := range out {
		seed ^= uint32(i)
		seed *= 16777619
		out[i] = byte(seed >> 24)
	}
	binary.LittleEndian.PutUint32(out[:4], seed)
	return out
}
