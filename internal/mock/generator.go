// Package mock implements a local, loopback-only RTSP camera source.
// Packet streams are generated deterministically from a seed and can
// be perturbed with drops, duplicates, reordering and latency.
package mock

import (
	"encoding/binary"
	"math/rand"

	"rtspcheck/internal/rtp"
)

// FaultConfig controls deterministic stream perturbation. The zero
// value produces a perfectly ordered, lossless stream.
type FaultConfig struct {
	// Seed makes every decision reproducible across runs.
	Seed int64
	// DropEvery drops 1 packet out of every N (N <= 1 disables).
	DropEvery int
	// DuplicateEvery re-sends 1 packet out of every N (0 disables).
	DuplicateEvery int
	// Reorder swaps adjacent output packets with this 0..1 probability.
	Reorder float64
	// Delay adds up to Delay jitter per packet (0 disables).
	Delay int
}

// PacketSpec is one generated datagram plus its send jitter.
type PacketSpec struct {
	// Data is a fully serialized RTP packet.
	Data []byte
	// Seq is its RTP sequence number (duplicates repeat it).
	Seq uint16
	// DelayJitter is the simulated per-packet delay in microseconds.
	DelayJitter int
}

const (
	videoPT        = 96
	videoSSRC      = 0x4d534d4b // "MSMK"
	packetsPerFrame = 4
	clockTicks      = 3600
	maxPayload      = 1200
)

// GenerateStream builds the deterministic packet schedule for
// frameCount frames. Every frame consists of packetsPerFrame RTP
// packets sharing one timestamp; the last packet carries the marker.
// The same (config, frameCount) pair always yields the same sequence,
// duplicates included.
func GenerateStream(frameCount int, cfg FaultConfig) []PacketSpec {
	rng := rand.New(rand.NewSource(cfg.Seed))
	total := frameCount * packetsPerFrame

	specs := make([]PacketSpec, 0, total)
	seq := uint16(0)
	for frame := 0; frame < frameCount; frame++ {
		ts := uint32(frame * clockTicks)
		for part := 0; part < packetsPerFrame; part++ {
			marker := part == packetsPerFrame-1

			if cfg.DropEvery > 1 && int(seq)%cfg.DropEvery == 0 {
				seq++
				continue
			}

			specs = append(specs, PacketSpec{
				Data:        serialize(seq, ts, marker, frame, part),
				Seq:         seq,
				DelayJitter: pickDelay(rng, cfg.Delay),
			})
			if cfg.DuplicateEvery > 1 && int(seq)%cfg.DuplicateEvery == 0 {
				specs = append(specs, PacketSpec{
					Data:        specs[len(specs)-1].Data,
					Seq:         seq,
					DelayJitter: pickDelay(rng, cfg.Delay),
				})
			}
			seq++
		}
	}

	if cfg.Reorder > 0 {
		// Adjacent swaps; deterministic because rng is seeded.
		for i := 0; i < len(specs)-1; i++ {
			if rng.Float64() < cfg.Reorder {
				specs[i], specs[i+1] = specs[i+1], specs[i]
			}
		}
	}
	return specs
}

func pickDelay(rng *rand.Rand, maxDelay int) int {
	if maxDelay <= 0 {
		return 0
	}
	return rng.Intn(maxDelay + 1)
}

// serialize builds a valid RTP packet (V2, no CSRC/extension/padding)
// with a small deterministic payload that records the frame/part.
func serialize(seq uint16, ts uint32, marker bool, frame, part int) []byte {
	payloadLen := 16 + (int(seq)%8)*4
	if payloadLen > maxPayload {
		payloadLen = maxPayload
	}
	buf := make([]byte, rtp.HeaderSize(0, 0, false)+payloadLen)
	buf[0] = 2 << 6
	if marker {
		buf[1] = 0x80 | videoPT
	} else {
		buf[1] = videoPT
	}
	binary.BigEndian.PutUint16(buf[2:4], seq)
	binary.BigEndian.PutUint32(buf[4:8], ts)
	binary.BigEndian.PutUint32(buf[8:12], videoSSRC)
	binary.BigEndian.PutUint32(buf[12:16], uint32(frame)<<16|uint32(part))
	for i := 16; i < len(buf); i++ {
		buf[i] = byte(seq) + byte(i)
	}
	return buf
}
