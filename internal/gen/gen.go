// Package gen produces the deterministic local fixtures used by the demo and
// the integration tests. No external data is read: PCM, RTP packets and MP4
// sample tables are all synthesized from a seed so every repeated run is
// byte-identical.
package gen

import (
	"encoding/binary"

	"github.com/pion/rtp"

	"mediacore"
)

// AudioConfig describes the generated single-channel-ish PCM/RTP fixture.
type AudioConfig struct {
	Seed       int64
	Rate       int
	Channels   int
	BlockSamps int // PCM frames per RTP block
	Blocks     int // number of blocks in the clean stream
	DriftPPM   int64
	WrapAt     uint32 // raw RTP timestamp at/near which a wrap is forced
	LateMS     int64  // receive delay injected on one block (0 disables)
	LateSeq    int    // block index made late
	DropSeqs   []int  // block indexes dropped on first arrival
}

// DefaultAudio returns the fixture tuning shared by the demo and tests.
func DefaultAudio(seed int64, driftPPM int64, wrapAt uint32, lateMS int64) AudioConfig {
	return AudioConfig{
		Seed: seed, Rate: 48000, Channels: 2, BlockSamps: 480, Blocks: 24,
		DriftPPM: driftPPM, WrapAt: wrapAt, LateMS: lateMS, LateSeq: 6,
		DropSeqs: []int{10, 17},
	}
}

// Packet is one generated RTP packet plus its simulated receive time and
// extended sequence metadata.
type Packet struct {
	Raw      []byte
	RecvNS   int64
	Seq16    uint16
	RTPTS    uint32
	BlockIdx int
	Dropped  bool
	Late     bool
}

// AudioFixture is the generated audio stream and its ground truth.
type AudioFixture struct {
	Cfg       AudioConfig
	Packets   []Packet // arrival order (drops omitted, late moved back)
	PCM       [][]byte // clean PCM payload per block, for ground truth
	BlockNS   int64    // duration of one block in integer nanoseconds
	LateBlock int
}

// BuildAudio synthesizes the audio fixture. Timestamps run on a drifting
// clock; a wrap is forced near Cfg.WrapAt; chosen blocks are dropped and one
// block is delivered late without disturbing the deterministic payloads.
func BuildAudio(cfg AudioConfig) *AudioFixture {
	f := &AudioFixture{Cfg: cfg, LateBlock: cfg.LateSeq}
	f.BlockNS = int64(cfg.BlockSamps) * mediacore.Second / int64(cfg.Rate)
	step := uint32(cfg.BlockSamps)
	// Start the raw timestamp just below the requested wrap point so the
	// 32-bit counter wraps part-way through the stream.
	startTS := cfg.WrapAt - uint32(cfg.Blocks/2)*step
	var ts uint32 = startTS
	recvBase := int64(0)
	recvStep := f.BlockNS // nominal one-block spacing on the receiver clock

	drop := map[int]bool{}
	for _, s := range cfg.DropSeqs {
		drop[s] = true
	}
	type staged struct {
		p Packet
	}
	all := make([]staged, 0, cfg.Blocks)
	for i := 0; i < cfg.Blocks; i++ {
		pcm := synthPCM(cfg.Seed, i, cfg.BlockSamps, cfg.Channels)
		f.PCM = append(f.PCM, pcm)
		pkt := &rtp.Packet{
			Header:  rtp.Header{SequenceNumber: uint16(i), Timestamp: ts, PayloadType: 96, SSRC: 1},
			Payload: pcm,
		}
		raw, _ := pkt.Marshal()
		recv := recvBase + int64(i)*recvStep
		p := Packet{Raw: raw, RecvNS: recv, Seq16: uint16(i), RTPTS: ts, BlockIdx: i}
		if drop[i] {
			p.Dropped = true
		}
		if i == cfg.LateSeq && cfg.LateMS > 0 {
			p.Late = true
			p.RecvNS += cfg.LateMS * mediacore.Millisecond
		}
		all = append(all, staged{p})
		ts += step // natural 32-bit overflow performs the wrap
	}
	// Emit in arrival order: dropped blocks are absent; the late block is
	// delivered immediately after its successor instead of at its own slot.
	for i := range all {
		if all[i].p.Dropped {
			continue
		}
		if all[i].p.Late {
			continue // re-emitted after the successor below
		}
		f.Packets = append(f.Packets, all[i].p)
		if cfg.LateMS > 0 && i == cfg.LateSeq-1 {
			f.Packets = append(f.Packets, all[cfg.LateSeq].p)
		}
	}
	return f
}

// synthPCM produces deterministic 16-bit PCM: a seed/block-dependent ramp so
// distinct blocks differ but repeats reproduce identical bytes.
func synthPCM(seed int64, block, frames, channels int) []byte {
	out := make([]byte, frames*channels*2)
	for i := 0; i < frames; i++ {
		v := int16(((seed*73 + int64(block)*101 + int64(i)*13) % 2001) - 1000)
		for c := 0; c < channels; c++ {
			binary.LittleEndian.PutUint16(out[(i*channels+c)*2:], uint16(v))
		}
	}
	return out
}

// VideoConfig describes the generated VFR video fixture.
type VideoConfig struct {
	Seed      int64
	Samples   int
	Timescale uint32
	Width     uint16
	Height    uint16
	VFR       bool // when true, frame durations alternate short/long
	KeyEvery  int  // a keyframe every N samples
}

// DefaultVideo returns the demo/test video tuning.
func DefaultVideo(seed int64, vfr bool) VideoConfig {
	return VideoConfig{Seed: seed, Samples: 12, Timescale: 90000,
		Width: 320, Height: 240, VFR: vfr, KeyEvery: 3}
}

// VideoFixture holds a generated sample table and its payload bytes.
type VideoFixture struct {
	Cfg       VideoConfig
	Deltas    []uint32
	Sizes     []uint32
	Keyframes []bool
	Data      []byte
}

// BuildVideo synthesizes a VFR/CFR video sample table with deterministic
// payloads and a regular keyframe cadence.
func BuildVideo(cfg VideoConfig) *VideoFixture {
	v := &VideoFixture{Cfg: cfg}
	baseDelta := uint32(cfg.Timescale / 30) // ~33.3 ms nominal
	for i := 0; i < cfg.Samples; i++ {
		d := baseDelta
		if cfg.VFR && i%2 == 1 {
			d = baseDelta * 2 // long frame on odd indexes
		}
		v.Deltas = append(v.Deltas, d)
		size := 64 + (i*37)%128
		v.Sizes = append(v.Sizes, uint32(size))
		v.Keyframes = append(v.Keyframes, i%cfg.KeyEvery == 0)
		frame := make([]byte, size)
		for j := range frame {
			frame[j] = byte((cfg.Seed*31 + int64(i)*17 + int64(j)) % 256)
		}
		v.Data = append(v.Data, frame...)
	}
	return v
}
