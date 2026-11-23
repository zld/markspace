package testdata

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/pion/rtp"
)

// RTPPacketSpec describes one generated RTP packet on the wire.
type RTPPacketSpec struct {
	Seq       uint16
	Timestamp uint32 // raw 32-bit, may wrap
	Marker    bool
	Payload   []byte
	// ReorderSwap makes the writer emit this packet after the following one.
	ReorderSwap bool
	// Late marks a packet whose arrival after commit must be quarantined.
	Late bool
}

// RTPWrapSpec controls wrap / drift / disorder synthesis.
type RTPWrapSpec struct {
	ClockRate   uint32 // typically 48000
	PacketSteps uint32 // timestamp increment per packet (typically 960 = 20ms)
	StartTS     uint32
	DriftPPM    float64
	Packets     int
	// Wrap forces a 32-bit rollover near the middle of the stream.
	Wrap bool
	// ReorderIndex is the packet index swapped with its successor.
	ReorderIndex int
	// LateIndex is an extra packet delivered after the stream committed.
	LateIndex int
}

// BuildRTPPackets synthesizes raw RTP packets from the wrap specification.
func BuildRTPPackets(s RTPWrapSpec) ([]RTPPacketSpec, *RTPPacketSpec) {
	if s.PacketSteps == 0 {
		s.PacketSteps = 960
	}
	pkts := make([]RTPPacketSpec, 0, s.Packets)
	ts := s.StartTS
	if s.Wrap {
		// Choose a start timestamp so the undisturbed step sequence crosses
		// the 32-bit ceiling halfway through the stream. The rollover then
		// happens naturally with no backwards jump in the packet sequence.
		ts = uint32((1 << 32) - int64(s.Packets/2)*int64(s.PacketSteps) -
			int64(s.PacketSteps)/2)
	}
	// Accumulated fractional drift in timestamp units. Real device drift is
	// sub-step per packet but accumulates, producing periodic +1 corrections;
	// carrying the remainder reproduces that instead of truncating to zero.
	var driftAccum float64
	for i := 0; i < s.Packets; i++ {
		driftAccum += float64(s.PacketSteps) * s.DriftPPM / 1_000_000.0
		corr := int64(driftAccum)
		driftAccum -= float64(corr)
		step := int64(s.PacketSteps) + corr
		if step <= 0 {
			step = int64(s.PacketSteps)
		}
		raw := ts
		payload := make([]byte, 4)
		binary.BigEndian.PutUint32(payload, uint32(i))
		pkts = append(pkts, RTPPacketSpec{
			Seq:       uint16(i),
			Timestamp: raw,
			Marker:    i == s.Packets-1,
			Payload:   payload,
		})
		ts = uint32(int64(ts) + step)
	}
	var late *RTPPacketSpec
	if s.LateIndex >= 0 && s.LateIndex < len(pkts) {
		cp := pkts[s.LateIndex]
		cp.Late = true
		late = &cp
	}
	if s.ReorderIndex >= 0 && s.ReorderIndex+1 < len(pkts) {
		pkts[s.ReorderIndex].ReorderSwap = true
	}
	return pkts, late
}

// WriteRTP writes a length-prefixed RTP capture file.
// Each record: 2-byte big-endian length followed by one RTP packet.
func WriteRTP(path string, pkts []RTPPacketSpec, late *RTPPacketSpec) error {
	var out []byte
	flush := func(p RTPPacketSpec) error {
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    111,
				SequenceNumber: p.Seq,
				Timestamp:      p.Timestamp,
				SSRC:           0x5eed42,
				Marker:         p.Marker,
			},
			Payload: p.Payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return err
		}
		hdr := make([]byte, 2)
		binary.BigEndian.PutUint16(hdr, uint16(len(raw)))
		out = append(out, hdr...)
		out = append(out, raw...)
		return nil
	}
	for i := range pkts {
		p := pkts[i]
		if p.ReorderSwap && i+1 < len(pkts) {
			if err := flush(pkts[i+1]); err != nil {
				return err
			}
		}
		if err := flush(p); err != nil {
			return err
		}
	}
	if late != nil {
		if err := flush(*late); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}
