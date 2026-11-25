package media

import (
	"encoding/binary"
	"fmt"
	"math/rand"

	"github.com/pion/rtp"
)

// BitrateConfig describes one rendition of the generated multi-bitrate program.
type BitrateConfig struct {
	Name              string
	Width, Height     int
	FrameCount        int
	FrameDurationTick int64 // ticks per frame on VideoTimescale
}

// Corruption selects which self-generated frame faults a fixture carries.
// Every field is an arrival index; zero values disable that fault.
type Corruption struct {
	TruncateAt    int // payload cut short -> recomputed hash differs
	BadChecksumAt int // payload byte flipped, declared hash kept
	DuplicateAt   int // frame reuses the previous frame's sequence number
	MissingAt     int // frame removed from delivery -> sequence gap
	OutOfRangeAt  int // frame sequence number forced negative
}

// FixtureConfig fully determines a generated program: same config, same bytes.
type FixtureConfig struct {
	CaseName   string
	Seed       int64
	Clock      Clock
	Renditions []BitrateConfig
	// Audio packet count and per-packet duration on the RTP clock.
	AudioPackets     int
	AudioFrameTicks  uint32
	AudioOffsetTicks uint32 // preset constant A/V offset on the audio clock
	LossRate         float64
	ReorderSwaps     int // adjacent (distance 2) delivery swaps
	MaxRecoverGap    int // recoverable contiguous loss run length
	Corrupt          Corruption
}

// Rendition is one bitrate's generated, metadata-complete frame sequence.
type Rendition struct {
	TaskKey string
	Config  BitrateConfig
	Frames  []Frame
}

// Fixture is the full generated program for one regression run.
type Fixture struct {
	Config     FixtureConfig
	Renditions []Rendition
	// AudioGround is the complete packet truth, including injected losses.
	AudioGround []AudioPacket
	// Delivered is what the ingest side actually receives after loss/reorder.
	Delivered []*rtp.Packet
	LostSeqs  []uint16
}

// DefaultConfig builds the standard 5 second, three-bitrate program.
func DefaultConfig(caseName string, seed int64) FixtureConfig {
	return FixtureConfig{
		CaseName: caseName,
		Seed:     seed,
		Clock: Clock{
			VideoTimescale: 1000, // 1 tick = 1 ms
			AudioClockRate: 48000,
		},
		Renditions: []BitrateConfig{
			{Name: "180p", Width: 96, Height: 54, FrameCount: 50, FrameDurationTick: 100},
			{Name: "360p", Width: 192, Height: 108, FrameCount: 50, FrameDurationTick: 100},
			{Name: "720p", Width: 384, Height: 216, FrameCount: 50, FrameDurationTick: 100},
		},
		AudioPackets:     250, // 250 * 20ms = 5s
		AudioFrameTicks:  960, // 20ms at 48kHz
		AudioOffsetTicks: 240, // preset 5ms A/V offset
		LossRate:         0.04,
		ReorderSwaps:     4,
		MaxRecoverGap:    1,
	}
}

// StableTaskKey returns the rerun-stable key for one rendition.
func StableTaskKey(caseName string, b BitrateConfig) string {
	return fmt.Sprintf("regress-%s-%s", caseName, b.Name)
}

// deterministicPayload expands a keyed SHA-256 stream into exactly size bytes.
func deterministicPayload(key [8]byte, size int) []byte {
	out := make([]byte, 0, size)
	var block uint32
	for len(out) < size {
		var buf [12]byte
		copy(buf[:8], key[:])
		binary.BigEndian.PutUint32(buf[8:], block)
		sum := ComputeSHA256(buf[:])
		take := size - len(out)
		if take > len(sum) {
			take = len(sum)
		}
		out = append(out, sum[:take]...)
		block++
	}
	return out
}

func frameKey(seed int64, brIdx, frameIdx, w, h int) [8]byte {
	var key [8]byte
	binary.BigEndian.PutUint32(key[:4], uint32(seed)^uint32(brIdx*7919+frameIdx))
	binary.BigEndian.PutUint32(key[4:], uint32(w*10000+h))
	return key
}

// audioPayload is intentionally tiny and fully determined by sequence number.
func audioPayload(seed int64, seq uint16) []byte {
	var key [8]byte
	binary.BigEndian.PutUint32(key[:4], uint32(seed))
	binary.BigEndian.PutUint32(key[4:], uint32(seq)*2654435761)
	return deterministicPayload(key, 32)
}

func buildFrames(cfg FixtureConfig, brIdx int, bc BitrateConfig) []Frame {
	frames := make([]Frame, bc.FrameCount)
	for i := 0; i < bc.FrameCount; i++ {
		payload := deterministicPayload(
			frameKey(cfg.Seed, brIdx, i, bc.Width, bc.Height), bc.Width*bc.Height)
		frames[i] = Frame{
			Index:          i,
			Seq:            i,
			TimestampTicks: int64(i) * bc.FrameDurationTick,
			Width:          bc.Width,
			Height:         bc.Height,
			Payload:        payload,
			DeclaredSHA256: ComputeSHA256(payload),
		}
	}
	return frames
}

// applyCorruption mutates one rendition's frames with the requested faults.
func applyCorruption(frames []Frame, c Corruption) []Frame {
	if c.TruncateAt > 0 && c.TruncateAt < len(frames) {
		f := frames[c.TruncateAt]
		f.Payload = f.Payload[:len(f.Payload)/2] // declared hash now stale
		frames[c.TruncateAt] = f
	}
	if c.BadChecksumAt > 0 && c.BadChecksumAt < len(frames) {
		f := frames[c.BadChecksumAt]
		f.Payload = append([]byte(nil), f.Payload...)
		f.Payload[0] ^= 0xFF // declared hash intentionally not updated
		frames[c.BadChecksumAt] = f
	}
	if c.OutOfRangeAt > 0 && c.OutOfRangeAt < len(frames) {
		f := frames[c.OutOfRangeAt]
		f.Seq = -1
		frames[c.OutOfRangeAt] = f
	}
	if c.DuplicateAt > 1 && c.DuplicateAt < len(frames) {
		f := frames[c.DuplicateAt]
		f.Seq = frames[c.DuplicateAt-1].Seq
		frames[c.DuplicateAt] = f
	}
	if c.MissingAt > 0 && c.MissingAt < len(frames) {
		frames = append(frames[:c.MissingAt], frames[c.MissingAt+1:]...)
	}
	return frames
}

// Generate deterministically builds the whole program described by cfg.
func Generate(cfg FixtureConfig) *Fixture {
	fx := &Fixture{Config: cfg}
	for i, bc := range cfg.Renditions {
		frames := buildFrames(cfg, i, bc)
		if i == 0 { // faults are concentrated on the first rendition
			frames = applyCorruption(frames, cfg.Corrupt)
		}
		fx.Renditions = append(fx.Renditions, Rendition{
			TaskKey: StableTaskKey(cfg.CaseName, bc),
			Config:  bc,
			Frames:  frames,
		})
	}

	rng := rand.New(rand.NewSource(cfg.Seed))
	ground := make([]AudioPacket, cfg.AudioPackets)
	lost := map[uint16]bool{}
	target := int(float64(cfg.AudioPackets) * cfg.LossRate)
	// Isolated losses only: neighbors always survive, so normal runs recover.
	for len(lost) < target {
		idx := 2 + rng.Intn(cfg.AudioPackets-4)
		seq := uint16(idx)
		if lost[seq] || lost[seq-1] || lost[seq+1] {
			continue
		}
		lost[seq] = true
	}
	for j := 0; j < cfg.AudioPackets; j++ {
		seq := uint16(j)
		ground[j] = AudioPacket{
			Seq:           seq,
			RTPTimestamp:  cfg.AudioOffsetTicks + uint32(j)*cfg.AudioFrameTicks,
			DurationTicks: cfg.AudioFrameTicks,
			Payload:       audioPayload(cfg.Seed, seq),
			InjectedLost:  lost[seq],
		}
	}
	fx.AudioGround = ground
	for s := range lost {
		fx.LostSeqs = append(fx.LostSeqs, s)
	}

	// Delivery order starts in sequence order, then gets bounded swaps.
	order := make([]int, 0, cfg.AudioPackets)
	for j := 0; j < cfg.AudioPackets; j++ {
		if !lost[uint16(j)] {
			order = append(order, j)
		}
	}
	for k := 0; k < cfg.ReorderSwaps; k++ {
		// Deterministic positions; displacement 2 stays inside tolerance 3.
		a := 4 + k*11
		b := a + 2
		if b < len(order) {
			order[a], order[b] = order[b], order[a]
		}
	}
	for _, j := range order {
		p := ground[j]
		fx.Delivered = append(fx.Delivered, &rtp.Packet{
			Header: rtp.Header{
				SequenceNumber: p.Seq,
				Timestamp:      p.RTPTimestamp,
				SSRC:           uint32(cfg.Seed) + 1,
				PayloadType:    96,
				Version:        2,
			},
			Payload: p.Payload,
		})
	}
	return fx
}

// GenerateUnrecoverable builds a fixture whose loss run exceeds the repair gap.
func GenerateUnrecoverable(cfg FixtureConfig) *Fixture {
	cfg.LossRate = 0 // suppress random isolated loss
	fx := Generate(cfg)
	// Drop three consecutive packets near the middle of the stream.
	delivered := make([]*rtp.Packet, 0, len(fx.Delivered)-3)
	for _, p := range fx.Delivered {
		if p.SequenceNumber >= 120 && p.SequenceNumber <= 122 {
			fx.AudioGround[p.SequenceNumber].InjectedLost = true
			continue
		}
		delivered = append(delivered, p)
	}
	fx.Delivered = delivered
	fx.LostSeqs = append(fx.LostSeqs, 120, 121, 122)
	return fx
}
