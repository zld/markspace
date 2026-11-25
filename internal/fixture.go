package kernel

import (
	"encoding/json"
	"fmt"

	"github.com/pion/rtp"
)

const (
	fixturePMTPID   uint16 = 0x1000
	fixtureVideoPID uint16 = 0x0100
	fixtureAudioPID uint16 = 0x0101
	fixtureDataPID  uint16 = 0x0102

	fixtureDurationMS int64 = 6000
	fixtureSampleMS   int64 = 100
	fixtureCutMS      int64 = 3000
)

// SourceSpec is the canonical immutable fixture source descriptor.
type SourceSpec struct {
	ID         string `json:"id"`
	Fixture    string `json:"fixture"`
	DurationMS int64  `json:"duration_ms"`
	SampleMS   int64  `json:"sample_ms"`
	Seed       int64  `json:"seed"`
	Mutated    bool   `json:"mutated"`
}

// NetworkDatagram is one generated arrival-order RTP datagram.
type NetworkDatagram struct {
	Raw          []byte `json:"-"`
	ArrivalIndex int    `json:"arrival_index"`
	OriginalSeq  uint16 `json:"original_seq"`
}

// Fixture contains generated source and network data; no external media is used.
type Fixture struct {
	Name            string
	MutateSource    bool
	Source          SourceSpec
	SourceCanonical []byte
	SourceDigest    string
	Datagrams       []NetworkDatagram
}

// GenerateFixture deterministically builds clean, damaged, or malformed input.
func GenerateFixture(name string, mutate bool) (*Fixture, error) {
	spec := SourceSpec{
		ID: "generated-event-replay", Fixture: name,
		DurationMS: fixtureDurationMS, SampleMS: fixtureSampleMS,
		Seed: 0x51a7, Mutated: mutate,
	}
	canonical, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	f := &Fixture{
		Name: name, MutateSource: mutate, Source: spec,
		SourceCanonical: canonical, SourceDigest: SourceDigest(canonical),
	}
	if name == "malformed" {
		f.Datagrams = []NetworkDatagram{{Raw: make([]byte, MaxPacketBytes+1), ArrivalIndex: 0}}
		return f, nil
	}

	tsPackets, err := buildTSPackets(name == "damaged", mutate)
	if err != nil {
		return nil, err
	}
	rtpPackets, err := packetizeRTP(tsPackets)
	if err != nil {
		return nil, err
	}
	f.Datagrams = impairArrival(rtpPackets, name == "damaged")
	return f, nil
}

func buildTSPackets(damaged, mutated bool) ([][]byte, error) {
	var out [][]byte
	cc := map[uint16]uint8{}
	add := func(pid uint16, payload []byte, pcr *uint64) error {
		seq := cc[pid]
		pkt, err := BuildTSPacket(pid, seq, payload, true, pcr)
		if err != nil {
			return err
		}
		cc[pid] = (seq + 1) % 16
		out = append(out, pkt)
		return nil
	}
	addJump := func(pid uint16, payload []byte, pcr *uint64) error {
		// cc[pid] is the next CC that would normally be used. Skip that value
		// to model one lost TS packet and send the following CC instead.
		wrong := (cc[pid] + 1) % 16
		pkt, err := BuildTSPacket(pid, wrong, payload, true, pcr)
		if err != nil {
			return err
		}
		cc[pid] = (wrong + 1) % 16
		out = append(out, pkt)
		return nil
	}

	pat := EncodePATSection(1, fixturePMTPID)
	if err := add(0x0000, TSPayloadWithPointer(pat), nil); err != nil {
		return nil, err
	}
	pmt := EncodePMTSection(1, fixtureVideoPID, []PMTStream{
		{PID: fixtureVideoPID, StreamType: 0x1b},
		{PID: fixtureAudioPID, StreamType: 0x0f},
		{PID: fixtureDataPID, StreamType: 0x06},
	})
	if err := add(fixturePMTPID, TSPayloadWithPointer(pmt), nil); err != nil {
		return nil, err
	}

	pcrStart := PCRBasePeriod - 3*ClockHz
	for tMS := int64(0); tMS < fixtureDurationMS; tMS += fixtureSampleMS {
		pts := uint64(tMS) * 90
		pcr := pcrStart + pts

		video, err := EncodeMessage(WireMessage{
			TrackKind: TrackVideo, PID: fixtureVideoPID, Index: uint32(tMS / fixtureSampleMS),
			PTS90k: pts, Duration90k: uint32(fixtureSampleMS * 90),
			Flags: map[bool]uint16{true: 1, false: 0}[tMS == 0 || tMS == fixtureCutMS],
			Data:  fixtureVideoBlock(tMS, mutated),
		})
		if err != nil {
			return nil, err
		}
		if err := add(fixtureVideoPID, video, &pcr); err != nil {
			return nil, err
		}

		audio, err := EncodeMessage(WireMessage{
			TrackKind: TrackAudio, PID: fixtureAudioPID, Index: uint32(tMS / fixtureSampleMS),
			PTS90k: pts, Duration90k: uint32(fixtureSampleMS * 90),
			Data: fixtureAudioBlock(tMS, mutated),
		})
		if err != nil {
			return nil, err
		}
		if damaged && tMS == 1200 {
			if err := addJump(fixtureAudioPID, audio, nil); err != nil {
				return nil, err
			}
		} else {
			if err := add(fixtureAudioPID, audio, nil); err != nil {
				return nil, err
			}
		}

		if damaged && tMS == 4200 {
			truncated, err := BuildTSPacket(fixtureAudioPID, cc[fixtureAudioPID], []byte("truncated"), true, nil)
			if err != nil {
				return nil, err
			}
			out = append(out, truncated[:120])
		}

		if tMS == fixtureCutMS {
			event, err := EncodeMessage(WireMessage{
				IsEvent: true, TrackKind: TrackData, PID: fixtureDataPID,
				Index: uint32(tMS / fixtureSampleMS), PTS90k: pts,
				Duration90k: uint32(fixtureSampleMS * 90), Label: "cut",
			})
			if err != nil {
				return nil, err
			}
			if err := add(fixtureDataPID, event, nil); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func fixtureAudioBlock(tMS int64, mutated bool) []byte {
	silent := tMS >= 1000 && tMS < 2000
	amp := int16(12000)
	if silent {
		amp = 0
	}
	if mutated && tMS == 5000 {
		amp = 11000
	}
	b := make([]byte, 20)
	for i := 0; i < 10; i++ {
		v := amp
		if !silent && i%2 == 0 {
			v = -amp
		}
		b[2*i] = byte(uint16(v))
		b[2*i+1] = byte(uint16(v) >> 8)
	}
	return b
}

func fixtureVideoBlock(tMS int64, mutated bool) []byte {
	b := []byte(fmt.Sprintf("frame-%04d", tMS/fixtureSampleMS))
	if mutated && tMS == 5000 {
		b = append(b, '-', 'm')
	}
	return b
}

func packetizeRTP(tsPackets [][]byte) ([][]byte, error) {
	out := make([][]byte, 0, len(tsPackets))
	seq := uint16(100)
	for i, ts := range tsPackets {
		pkt := &rtp.Packet{Header: rtp.Header{
			Version: 2, PayloadType: 33, SequenceNumber: seq + uint16(i),
			Timestamp: uint32(i) * 9000, SSRC: 0x52504b31,
		}, Payload: ts}
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

func impairArrival(rtpPackets [][]byte, damaged bool) []NetworkDatagram {
	if !damaged {
		out := make([]NetworkDatagram, len(rtpPackets))
		for i, raw := range rtpPackets {
			out[i] = NetworkDatagram{Raw: raw, ArrivalIndex: i}
		}
		return out
	}
	var out []NetworkDatagram
	for i, raw := range rtpPackets {
		if i == 10 {
			continue
		}
		if i == 11 {
			out = append(out, NetworkDatagram{Raw: rtpPackets[11], ArrivalIndex: len(out), OriginalSeq: uint16(100 + 11)})
			out = append(out, NetworkDatagram{Raw: rtpPackets[10], ArrivalIndex: len(out), OriginalSeq: uint16(100 + 10)})
			continue
		}
		out = append(out, NetworkDatagram{Raw: raw, ArrivalIndex: len(out), OriginalSeq: uint16(100 + i)})
		if i == 5 {
			out = append(out, NetworkDatagram{Raw: raw, ArrivalIndex: len(out), OriginalSeq: uint16(100 + i)})
		}
	}
	return out
}
