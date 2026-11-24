// Package fixture generates synthetic CMAF/fMP4 inputs and their expected
// records. All sample data is generated from a seeded PRNG; nothing is read
// from external media. Each fixture is self-describing so tests can assert on
// both the damage that was injected and the repair that must result.
package fixture

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
)

// Params describes one generated stream.
type Params struct {
	Name           string
	Seed           int64
	SegmentMS      uint32 // nominal segment duration in milliseconds
	Fragments      int
	SamplesPerFrag int
	VideoTimescale uint32
	AudioTimescale uint32
	VideoBitrate   uint32 // approximate bytes per second contribution
	AudioBitrate   uint32
	Width          uint32
	Height         uint32
	// TargetBPS, when positive, overrides the bitrate split: sample sizes are
	// solved so the whole delivered file (init, moof overhead and mdat) lands
	// within 5% of this total bitrate.
	TargetBPS int64
}

// DefaultParams returns a two-track CMAF layout used by the demo.
func DefaultParams(seed int64) Params {
	return Params{
		Name:           "demo",
		Seed:           seed,
		SegmentMS:      1000,
		Fragments:      6,
		SamplesPerFrag: 5,
		VideoTimescale: 90000,
		AudioTimescale: 48000,
		VideoBitrate:   1_500_000,
		AudioBitrate:   128_000,
		Width:          1280,
		Height:         720,
	}
}

// Damage selects which anomaly a variant injects.
type Damage int

const (
	DamageNone Damage = iota
	DamageSequenceGap
	DamageTfdtRegression
	DamageTruncatedBox
	DamageCorruptIndex
	DamageMissingInit
	DamageZeroTimescale
)

func (d Damage) String() string {
	switch d {
	case DamageNone:
		return "clean"
	case DamageSequenceGap:
		return "sequence-gap"
	case DamageTfdtRegression:
		return "tfdt-regression"
	case DamageTruncatedBox:
		return "truncated-box"
	case DamageCorruptIndex:
		return "corrupt-index"
	case DamageMissingInit:
		return "missing-init"
	case DamageZeroTimescale:
		return "zero-timescale"
	}
	return "unknown"
}

// Generated is a fixture and the facts a test should be able to verify about
// it without re-parsing.
type Generated struct {
	Params       Params
	Damage       Damage
	Bytes        []byte
	SegmentDurUs int64
	// Fragments describes what the source should yield before repair.
	Fragments []FragmentSpec
	// Expected valid sample count across all tracks if damage is survived.
	ExpectedValidSamples int
	ExpectedDropped      int
	TruncatedBoxes       int
	CorruptIndexOffset   uint64
}

// FragmentSpec is the ground truth for one generated fragment.
type FragmentSpec struct {
	Sequence     uint32
	VideoTfdt    uint64
	AudioTfdt    uint64
	VideoSamples int
	AudioSamples int
	Truncated    bool
}

// prng is a small deterministic xorshift generator so fixtures never depend
// on math/rand version differences.
type prng struct{ state uint64 }

func newPRNG(seed int64) *prng {
	s := uint64(seed) ^ 0x9E3779B97F4A7C15
	if s == 0 {
		s = 0xDEADBEEFCAFEBABE
	}
	return &prng{state: s}
}

func (p *prng) next() uint64 {
	x := p.state
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	p.state = x
	return x
}

func (p *prng) bytes(n int) []byte {
	out := make([]byte, n)
	var buf [8]byte
	for i := 0; i < n; i += 8 {
		binary.BigEndian.PutUint64(buf[:], p.next())
		copy(out[i:], buf[:])
	}
	return out
}

// samplePayload produces deterministic pseudo-random sample bytes. The hash
// of (seed, track, size) keys the generator so payloads differ per sample
// while staying reproducible across runs.
func samplePayload(seed int64, track byte, size int) []byte {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d:%c:%d", seed, track, size)
	p := &prng{state: h.Sum64() | 1}
	return p.bytes(size)
}
