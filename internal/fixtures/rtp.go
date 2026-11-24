package fixtures

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/pion/rtp"
)

// RTPSpec describes a synthetic RTP timestamp stream.
type RTPSpec struct {
	Timescale    uint32
	RefTimescale uint32
	SSRC         uint32
	PayloadType  uint8
	Count        int
	StartSeq     uint16
	StartTS      uint32
	// StepTS is the nominal media advance per packet.
	StepTS uint32
	// StepRef is the reference-clock advance per packet, in RefTimescale
	// units. Its relation to StepTS is what creates drift.
	StepRef uint32
	// DriftPPM scales the media step by (1 + ppm/1e6). Negative slows the
	// media clock down. It is computed with integer math so the fixture is
	// exactly reproducible.
	DriftPPM int64
}

// RTPStream returns a binary stream: magic, a fixed header, then
// length-prefixed RTP packets each followed by a uint32 reference tick.
//
//	bytes 0..5   "MAUD1\n"
//	u32 timescale
//	u32 reference timescale
//	u32 count
//	u32 ssrc
//	per packet: u32 length, packet bytes, u32 reference ticks
func RTPStream(spec RTPSpec) ([]byte, error) {
	if spec.Count <= 0 {
		return nil, errors.New("fixtures: RTP stream needs at least one packet")
	}
	if spec.Timescale == 0 || spec.RefTimescale == 0 || spec.StepTS == 0 || spec.StepRef == 0 {
		return nil, errors.New("fixtures: RTP timescales and steps must be non-zero")
	}
	if spec.PayloadType == 0 {
		spec.PayloadType = 96
	}
	if spec.SSRC == 0 {
		spec.SSRC = 0x0badcafe
	}
	out := []byte("MAUD1\n")
	for _, v := range []uint32{spec.Timescale, spec.RefTimescale, uint32(spec.Count), spec.SSRC} {
		out = append(out, be32(v)...)
	}
	ts := spec.StartTS
	for i := 0; i < spec.Count; i++ {
		seq := spec.StartSeq + uint16(i)
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    spec.PayloadType,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           spec.SSRC,
			},
			Payload: []byte{byte(i >> 8), byte(i)},
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, fmt.Errorf("fixtures: marshal RTP packet %d: %w", i, err)
		}
		out = append(out, be32(uint32(len(raw)))...)
		out = append(out, raw...)
		out = append(out, be32(uint32(i)*spec.StepRef)...)
		step := uint64(spec.StepTS)
		if spec.DriftPPM != 0 {
			// media step = StepTS * (1e6 + ppm) / 1e6, truncated toward zero.
			num := int64(spec.StepTS) * (1_000_000 + spec.DriftPPM)
			step = uint64(num / 1_000_000)
		}
		ts += uint32(step)
	}
	return out, nil
}

// RTPSample is one decoded record from a generated stream.
type RTPSample struct {
	Packet  *rtp.Packet
	RefTick uint32
}

// RTPStreamHeader is the fixed header of a generated stream.
type RTPStreamHeader struct {
	Timescale    uint32
	RefTimescale uint32
	Count        uint32
	SSRC         uint32
}

// ParseRTPStream decodes a stream written by RTPStream. It is used by the
// pipeline and by tests, and it exercises pion/rtp unmarshalling.
func ParseRTPStream(data []byte) (RTPStreamHeader, []RTPSample, error) {
	var h RTPStreamHeader
	if len(data) < 6 || string(data[:6]) != "MAUD1\n" {
		return h, nil, errors.New("fixtures: bad RTP stream magic")
	}
	if len(data) < 6+16 {
		return h, nil, errors.New("fixtures: truncated RTP stream header")
	}
	off := 6
	h.Timescale = binary.BigEndian.Uint32(data[off:])
	h.RefTimescale = binary.BigEndian.Uint32(data[off+4:])
	h.Count = binary.BigEndian.Uint32(data[off+8:])
	h.SSRC = binary.BigEndian.Uint32(data[off+12:])
	off += 16
	samples := make([]RTPSample, 0, h.Count)
	for i := uint32(0); i < h.Count; i++ {
		if off+4 > len(data) {
			return h, nil, fmt.Errorf("fixtures: truncated length for packet %d", i)
		}
		n := binary.BigEndian.Uint32(data[off:])
		off += 4
		if off+int(n)+4 > len(data) {
			return h, nil, fmt.Errorf("fixtures: truncated packet %d", i)
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(data[off : off+int(n)]); err != nil {
			return h, nil, fmt.Errorf("fixtures: unmarshal packet %d: %w", i, err)
		}
		off += int(n)
		ref := binary.BigEndian.Uint32(data[off:])
		off += 4
		samples = append(samples, RTPSample{Packet: &pkt, RefTick: ref})
	}
	return h, samples, nil
}
