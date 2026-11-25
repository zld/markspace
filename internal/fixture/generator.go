// Package fixture deterministically synthesizes the RTP/RTCP packet
// streams used by tests and the demo command. Every generator takes an
// explicit seed so the same seed always yields byte-identical output;
// no network, clock or filesystem entropy is involved.
package fixture

import (
	"encoding/binary"
	"math/rand"

	"github.com/pion/rtp"

	rtppkg "mediacheck/internal/rtp"
	"mediacheck/internal/stream"
)

// DefaultClockRate is the 90kHz video clock used by every synthetic track.
const DefaultClockRate = 90000

// FrameTicks is the 40ms frame duration at 90kHz.
const FrameTicks = 3600

// Scenario names exercised by the demo and acceptance tests.
const (
	ScenarioNormal        = "normal"
	ScenarioReorderLoss   = "reorder_loss"
	ScenarioTruncatedNAL  = "truncated_nal"
	ScenarioReplace       = "same_path_replace"
	ScenarioMetadata      = "metadata_update"
	ScenarioCommitAbort   = "commit_interrupt"
)

// Datagram is one generated transport datagram in arrival order.
type Datagram struct {
	Raw       []byte
	IsRTCP    bool
	SeqHint   uint16
	Arrival   int
}

// Stream is a fully generated synthetic track.
type Stream struct {
	Codec      stream.Codec
	SSRC       uint32
	ClockRate  uint32
	Packets    []*rtp.Packet
	RTCP       []Datagram
	Datagrams  []Datagram
	// Ground truth for acceptance assertions.
	SampleCount   int
	KeyFrameEvery int
	Seed          int64
}

// Generator builds deterministic streams.
type Generator struct {
	rng       *rand.Rand
	ssrc      uint32
	clockRate uint32
}

// NewGenerator seeds the deterministic RNG. Seed 7 is the demo default.
func NewGenerator(seed int64) *Generator {
	return &Generator{
		rng:       rand.New(rand.NewSource(seed)),
		ssrc:      1000 + uint32(seed%9000),
		clockRate: DefaultClockRate,
	}
}

// frameNALs returns the NAL units for one frame: an IDR slice every
// keyEvery frames, otherwise a P slice, with deterministic payload bytes.
func (g *Generator) frameNALs(codec stream.Codec, frame int, keyEvery int) [][]byte {
	key := frame%keyEvery == 0
	if codec == stream.CodecH264 {
		if key {
			return [][]byte{{0x65, byte(frame), 0xAA, 0xBB}}
		}
		return [][]byte{{0x41, byte(frame), 0xCC}}
	}
	if key {
		// HEVC IDR_W_RADL type 19 in bits 1..6.
		return [][]byte{{0x26, byte(frame), 0xAA}}
	}
	// HEVC TRAIL_R type 1.
	return [][]byte{{0x02, byte(frame), 0xCC}}
}

// packetize turns one NAL unit into RTP packets, splitting payloads
// larger than maxPayload into FU packets; the final packet carries the
// marker bit.
func (g *Generator) packetize(codec stream.Codec, nals [][]byte, ts uint32, seq *uint16) []*rtp.Packet {
	const maxPayload = 1200
	out := make([]*rtp.Packet, 0)
	for _, nal := range nals {
		if len(nal) <= maxPayload {
			out = append(out, g.mkPacket(*seq, ts, false, nal))
			*seq = *seq + 1
			continue
		}
		out = append(out, g.fragment(codec, nal, ts, seq, maxPayload)...)
	}
	out[len(out)-1].Marker = true
	return out
}

func (g *Generator) fragment(codec stream.Codec, nal []byte, ts uint32, seq *uint16, mtu int) []*rtp.Packet {
	out := make([]*rtp.Packet, 0)
	if codec == stream.CodecH264 {
		indicator := (nal[0] & 0xE0) | 28
		nalType := nal[0] & 0x1F
		body := nal[1:]
		first := true
		for len(body) > 0 {
			n := mtu - 2
			if n > len(body) {
				n = len(body)
			}
			fuByte := nalType
			switch {
			case first:
				fuByte |= 0x80
			case n == len(body):
				fuByte |= 0x40
			}
			payload := append([]byte{indicator, fuByte}, body[:n]...)
			out = append(out, g.mkPacket(*seq, ts, false, payload))
			*seq = *seq + 1
			body = body[n:]
			first = false
		}
		return out
	}
	// HEVC FU: two-byte payload header (type 49), FU byte S/E + low-6 type.
	indicator := []byte{(nal[0] & 0x81) | (49 << 1), 1}
	nalType := (nal[0] >> 1) & 0x3F
	body := nal[1:]
	first := true
	for len(body) > 0 {
		n := mtu - 3
		if n > len(body) {
			n = len(body)
		}
		fuByte := nalType
		switch {
		case first:
			fuByte |= 0x80
		case n == len(body):
			fuByte |= 0x40
		}
		payload := append(append([]byte{}, indicator...), fuByte)
		payload = append(payload, body[:n]...)
		out = append(out, g.mkPacket(*seq, ts, false, payload))
		*seq = *seq + 1
		body = body[n:]
		first = false
	}
	return out
}

func (g *Generator) mkPacket(seq uint16, ts uint32, marker bool, payload []byte) *rtp.Packet {
	raw, err := rtppkg.EncodeDatagram(&rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SSRC: g.ssrc,
			SequenceNumber: seq, Timestamp: ts, Marker: marker,
		},
		Payload: append([]byte{}, payload...),
	})
	if err != nil {
		panic(err)
	}
	p := &rtp.Packet{}
	if err := p.Unmarshal(raw); err != nil {
		panic(err)
	}
	return p
}

// Normal builds a clean, in-order stream with frames frames and an IDR
// every keyEvery frames. Expected loss and damage counts are both zero.
func (g *Generator) Normal(codec stream.Codec, frames, keyEvery int) *Stream {
	s := &Stream{Codec: codec, SSRC: g.ssrc, ClockRate: g.clockRate,
		SampleCount: frames, KeyFrameEvery: keyEvery, Seed: g.rng.Int63()}
	var seq uint16
	for f := 0; f < frames; f++ {
		ts := uint32(f * FrameTicks)
		nals := g.frameNALs(codec, f, keyEvery)
		s.Packets = append(s.Packets, g.packetize(codec, nals, ts, &seq)...)
	}
	s.attachReports()
	s.materialize()
	return s
}

// attachReports emits one sender report halfway through the stream so
// every normal track has a trusted wall-clock anchor.
func (s *Stream) attachReports() {
	if len(s.Packets) == 0 {
		return
	}
	mid := s.Packets[len(s.Packets)/2]
	sr := rtppkg.SenderReport{
		SSRC:         s.SSRC,
		RTPTimestamp: mid.Timestamp,
		NTPSeconds:   1000.0 + float64(mid.Timestamp)/float64(s.ClockRate),
		PacketCount:  uint32(len(s.Packets)),
	}
	s.RTCP = []Datagram{{Raw: rtppkg.BuildSenderReport(sr), IsRTCP: true}}
}

func (s *Stream) materialize() {
	for i, p := range s.Packets {
		raw, err := rtppkg.EncodeDatagram(p)
		if err != nil {
			panic(err)
		}
		s.Datagrams = append(s.Datagrams, Datagram{
			Raw: raw, SeqHint: p.SequenceNumber, Arrival: i,
		})
	}
	s.Datagrams = append(s.Datagrams, s.RTCP...)
}

// ReorderAndLoss shuffles adjacent packets within swapWindow positions
// and drops the sequence numbers listed in droppedSeqs (raw seqs).
func (s *Stream) ReorderAndLoss(swapWindow int, droppedSeqs ...uint16) {
	drop := map[uint16]bool{}
	for _, q := range droppedSeqs {
		drop[q] = true
	}
	kept := make([]*rtp.Packet, 0, len(s.Packets))
	for _, p := range s.Packets {
		if !drop[p.SequenceNumber] {
			kept = append(kept, p)
		}
	}
	// Deterministic adjacent swaps driven by the generator's RNG.
	for i := 1; i < len(kept); i++ {
		if i%swapWindow == 0 {
			kept[i-1], kept[i] = kept[i], kept[i-1]
		}
	}
	s.Packets = kept
	s.materialize()
}

// TruncateNAL cuts the payload of the packet whose raw seq equals seq:
// only head bytes survive, simulating a truncated NAL in flight.
func (s *Stream) TruncateNAL(seq uint16, head int) {
	for _, p := range s.Packets {
		if p.SequenceNumber == seq {
			if head > len(p.Payload) {
				head = len(p.Payload)
			}
			p.Payload = append([]byte{}, p.Payload[:head]...)
		}
	}
	s.materialize()
}

// CorruptLength rewrites a FU length field region by zeroing the tail of
// one packet, producing an illegal-length style fault at a known byte.
func (s *Stream) CorruptLength(seq uint16) {
	for _, p := range s.Packets {
		if p.SequenceNumber == seq && len(p.Payload) > 4 {
			p.Payload = p.Payload[:2]
		}
	}
	s.materialize()
}

// PayloadBytes flattens every packet payload for content hashing.
func PayloadBytes(packets []*rtp.Packet) [][]byte {
	out := make([][]byte, len(packets))
	for i, p := range packets {
		out[i] = append([]byte{}, p.Payload...)
	}
	return out
}

// HeaderBytes flattens RTP header bytes for container hashing.
func HeaderBytes(packets []*rtp.Packet) [][]byte {
	out := make([][]byte, len(packets))
	for i, p := range packets {
		out[i] = make([]byte, 12)
		binary.BigEndian.PutUint16(out[i][2:], p.SequenceNumber)
		binary.BigEndian.PutUint32(out[i][4:], p.Timestamp)
		out[i][1] = p.PayloadType
	}
	return out
}
