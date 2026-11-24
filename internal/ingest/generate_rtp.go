package ingest

import (
	"encoding/binary"
	"fmt"
	"math/rand"

	"github.com/pion/rtp"
)

// RTPGenOptions controls deterministic audio packet generation.
type RTPGenOptions struct {
	Path             string
	Seed             int64
	Packets          int
	SamplesPerPacket uint32
	Channels         int
	SampleRate       uint32
	SSRC             uint32
	StartSequence    uint16
	Late             bool
	Duplicate        bool
	OutOfOrder       bool
}

// RTPFixture records generated packet ordering facts.
type RTPFixture struct {
	Path             string `json:"path"`
	CanonicalPackets int    `json:"canonical_packets"`
	StoredRecords    int    `json:"stored_records"`
	SamplesPerPacket uint32 `json:"samples_per_packet"`
	Channels         int    `json:"channels"`
	SampleRate       uint32 `json:"sample_rate"`
	LateSequence     int    `json:"late_sequence,omitempty"`
	DuplicateSequence int   `json:"duplicate_sequence,omitempty"`
	Offsets          []int64 `json:"-"`
}

// GenerateRTPFile creates L16-like deterministic packets and injects ordering
// faults by changing container order, not RTP sequence numbers.
func GenerateRTPFile(o RTPGenOptions) (*RTPFixture, error) {
	if o.Packets <= 0 {
		return nil, fmt.Errorf("packets must be positive")
	}
	if o.SamplesPerPacket == 0 {
		o.SamplesPerPacket = 480 // 10 ms at 48 kHz
	}
	if o.Channels == 0 {
		o.Channels = 2
	}
	if o.SampleRate == 0 {
		o.SampleRate = 48000
	}
	if o.SSRC == 0 {
		o.SSRC = 0x171717
	}
	rng := rand.New(rand.NewSource(o.Seed + 101))
	canonical := make([]*rtp.Packet, o.Packets)
	for i := 0; i < o.Packets; i++ {
		seq := o.StartSequence + uint16(i)
		ts := uint32(i) * o.SamplesPerPacket
		payload := make([]byte, int(o.SamplesPerPacket)*o.Channels*2)
		for s := 0; s < int(o.SamplesPerPacket)*o.Channels; s++ {
			v := int16((i*31 + s*7 + int(rng.Intn(13))) & 0xffff)
			binary.BigEndian.PutUint16(payload[2*s:], uint16(v))
		}
		canonical[i] = NewAudioRTPPacket(seq, ts, o.SSRC, payload)
	}

	order := make([]*rtp.Packet, 0, o.Packets+2)
	for _, p := range canonical {
		order = append(order, p)
	}
	fx := &RTPFixture{Path: o.Path, CanonicalPackets: o.Packets, SamplesPerPacket: o.SamplesPerPacket,
		Channels: o.Channels, SampleRate: o.SampleRate, DuplicateSequence: -1, LateSequence: -1}

	if o.OutOfOrder && o.Packets >= 8 {
		for _, frac := range []int{4, 2} {
			p := o.Packets / frac
			if p+1 < len(order) {
				order[p], order[p+1] = order[p+1], order[p]
			}
		}
	}
	if o.Late && o.Packets >= 32 {
		idx := o.Packets / 3
		delay := 12 + int(rng.Intn(5))
		if idx+delay >= len(order) {
			delay = 8
		}
		pkt := order[idx]
		order = append(order[:idx], order[idx+1:]...)
		insertAt := idx + delay - 1
		if insertAt > len(order) {
			insertAt = len(order)
		}
		order = append(order, nil)
		copy(order[insertAt+1:], order[insertAt:])
		order[insertAt] = pkt
		fx.LateSequence = int(pkt.SequenceNumber)
	}
	if o.Duplicate && o.Packets >= 10 {
		idx := o.Packets / 5
		dup := canonical[idx]
		at := idx + 3
		if at > len(order) {
			at = len(order)
		}
		order = append(order, nil)
		copy(order[at+1:], order[at:])
		order[at] = dup
		fx.DuplicateSequence = int(dup.SequenceNumber)
	}

	offsets, err := WriteRTPFile(o.Path, order)
	if err != nil {
		return nil, err
	}
	fx.Offsets = offsets
	fx.StoredRecords = len(order)
	return fx, nil
}
