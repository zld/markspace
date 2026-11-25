// Package demo generates the fully synthetic, deterministic acceptance
// fixtures: dual-channel PCM/WAV audio and an RTP packet stream that
// contains exactly one duplicate, deliberate reordering and a burst
// loss. No external media assets are referenced.
package demo

import (
	"math"

	pionrtp "github.com/pion/rtp"

	"continuity/internal/media"
	"continuity/internal/rtp"
)

const (
	// SampleRate is the fixture sample rate.
	SampleRate = 48000
	// Channels is the fixture channel count (stereo).
	Channels = 2
	// Bits is the fixture bit depth.
	Bits = 16
	// SamplesPerPacket is the fixed number of frames per RTP packet.
	SamplesPerPacket = 16
	// PacketCount is how many packets the clean stream contains.
	PacketCount = 24
	// BlockFrames is the offline processing block size.
	BlockFrames = 32
)

// PCM returns the deterministic stereo PCM for the whole stream. The
// waveform stays well inside full scale so peak dBFS lands in [-60, 0].
func PCM(seed int64) []byte {
	totalFrames := PacketCount * SamplesPerPacket
	out := make([]byte, totalFrames*Channels*(Bits/8))
	amp := 0.25
	for i := 0; i < totalFrames; i++ {
		t := float64(i) / float64(SampleRate)
		l := math.Sin(2*math.Pi*440*t) * amp
		r := math.Sin(2*math.Pi*660*t+float64(seed)) * amp
		putS16(out[i*4:], l)
		putS16(out[i*4+2:], r)
	}
	return out
}

func putS16(b []byte, v float64) {
	s := int(math.Round(v * 32767))
	if s > 32767 {
		s = 32767
	} else if s < -32768 {
		s = -32768
	}
	b[0] = byte(uint16(s))
	b[1] = byte(uint16(s) >> 8)
}

// Packet is one generated datagram together with its sequence number.
type Packet struct {
	Seq     uint16
	Raw     []byte
	Payload []byte
}

// packets builds the clean, in-order packet set.
func packets(seed int64) []Packet {
	pcm := PCM(seed)
	frameBytes := Channels * (Bits / 8)
	out := make([]Packet, PacketCount)
	for i := 0; i < PacketCount; i++ {
		start := i * SamplesPerPacket * frameBytes
		payload := pcm[start : start+SamplesPerPacket*frameBytes]
		p := &pionrtp.Packet{
			Header: pionrtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      uint32(i * SamplesPerPacket),
				SSRC:           0xC0FFEE,
			},
			Payload: append([]byte(nil), payload...),
		}
		raw, _ := p.Marshal()
		out[i] = Packet{Seq: uint16(i), Raw: raw, Payload: append([]byte(nil), payload...)}
	}
	return out
}

// StreamPlan describes the fault injection applied to the clean stream.
type StreamPlan struct {
	// DuplicateSeq is sent twice.
	DuplicateSeq uint16
	// ReorderSwap exchanges the arrival order of two adjacent packets.
	ReorderSwap [2]uint16
	// DropSeqs are burst-lost sequence numbers.
	DropSeqs []uint16
}

// DefaultPlan produces exactly one duplicate, visible reordering and one
// burst gap while keeping enough packets for a complete demo.
func DefaultPlan() StreamPlan {
	return StreamPlan{
		DuplicateSeq: 6,
		ReorderSwap:  [2]uint16{10, 11},
		DropSeqs:     []uint16{18, 19},
	}
}

// Generate builds the arrival-order datagram list for the given plan.
func Generate(seed int64, plan StreamPlan) []Packet {
	clean := packets(seed)
	drop := map[uint16]bool{}
	for _, s := range plan.DropSeqs {
		drop[s] = true
	}
	var arrival []Packet
	for i := range clean {
		if drop[uint16(i)] {
			continue
		}
		arrival = append(arrival, clean[i])
		if uint16(i) == plan.DuplicateSeq {
			// Exact retransmission: same seq, same bytes.
			arrival = append(arrival, clean[i])
		}
	}
	swap(arrival, plan.ReorderSwap[0], plan.ReorderSwap[1])
	return arrival
}

// swap exchanges two packets by sequence number inside the arrival list.
func swap(arrival []Packet, a, b uint16) {
	ia, ib := -1, -1
	for i, p := range arrival {
		if p.Seq == a {
			ia = i
		}
		if p.Seq == b {
			ib = i
		}
	}
	if ia >= 0 && ib >= 0 {
		arrival[ia], arrival[ib] = arrival[ib], arrival[ia]
	}
}

// Rebuild runs the generated arrival stream through the RTP ingest and
// returns reconstructed blocks plus transport stats.
func Rebuild(arrival []Packet) ([]rtp.Block, rtp.Stats, int64, error) {
	clock := media.NewClock(SampleRate)
	in := rtp.NewIngest(clock, rtp.Config{
		ClockRate:     SampleRate,
		Channels:      Channels,
		BitsPerSample: Bits,
		WindowWidth:   8,
		BlockFrames:   BlockFrames,
	})
	for _, p := range arrival {
		if _, err := in.PushPacket(p.Raw); err != nil {
			return nil, rtp.Stats{}, 0, err
		}
	}
	blocks, st, err := in.Flush()
	return blocks, st, in.MaxAlignmentErrorSamples(), err
}
