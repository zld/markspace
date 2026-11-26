// Package rtphint parses RTP hint-track packet headers and validates sequence
// number progression, including uint16 rollover.
package rtphint

import (
	"fmt"

	"github.com/pion/rtp"

	"mediapipeline/pkg/media"
)

// Header is the validated subset of an RTP hint packet header.
type Header struct {
	Version        uint8  `json:"version"`
	Padding        bool   `json:"padding"`
	Marker         bool   `json:"marker"`
	PayloadType    uint8  `json:"payload_type"`
	SequenceNumber uint16 `json:"sequence_number"`
	ExtendedSeq    uint64 `json:"extended_sequence"`
	Timestamp      uint32 `json:"timestamp"`
	SSRC           uint32 `json:"ssrc"`
	CSRCCount      int    `json:"csrc_count"`
}

// SequenceReport contains unwrapped sequence state and anomalies.
type SequenceReport struct {
	Packets    int            `json:"packets"`
	FirstSeq   uint16         `json:"first_seq"`
	LastSeq    uint16         `json:"last_seq"`
	LastExtSeq uint64         `json:"last_ext_seq"`
	WrapCount  int            `json:"wrap_count"`
	Gaps       int            `json:"gaps"`
	Backwards  int            `json:"backwards"`
	Duplicates int            `json:"duplicates"`
	Faults     []*media.Fault `json:"faults,omitempty"`
}

// ParseHeader unmarshals one raw RTP packet using github.com/pion/rtp.
func ParseHeader(raw []byte) (Header, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return Header{}, media.NewFault(
			media.ClassRTPBadVersion,
			media.SeverityError,
			"invalid RTP header",
			media.WithBox("rtp", 0),
			media.WithCause(err),
		)
	}
	return Header{
		Version:        pkt.Version,
		Padding:        pkt.Padding,
		Marker:         pkt.Marker,
		PayloadType:    pkt.PayloadType,
		SequenceNumber: pkt.SequenceNumber,
		Timestamp:      pkt.Timestamp,
		SSRC:           pkt.SSRC,
		CSRCCount:      len(pkt.CSRC),
	}, nil
}

// ValidateSequence parses raw RTP packets in hint-track order and unwraps
// uint16 sequence numbers. The 65535 -> 0 transition is treated as a wrap, not
// a regression; smaller backward deltas are reported.
func ValidateSequence(rawPackets [][]byte) (SequenceReport, error) {
	rep := SequenceReport{Packets: len(rawPackets)}
	if len(rawPackets) == 0 {
		return rep, nil
	}

	var prevRaw uint16
	var prevExt int64
	have := false
	for i, raw := range rawPackets {
		h, err := ParseHeader(raw)
		if err != nil {
			f, _ := media.AsFault(err)
			if f != nil {
				f.Message = fmt.Sprintf("packet %d: %s", i, f.Message)
				rep.Faults = append(rep.Faults, f)
			}
			return rep, err
		}

		if !have {
			rep.FirstSeq = h.SequenceNumber
			rep.LastSeq = h.SequenceNumber
			rep.LastExtSeq = uint64(h.SequenceNumber)
			prevRaw = h.SequenceNumber
			prevExt = int64(h.SequenceNumber)
			have = true
			continue
		}

		// Modular signed delta. This is the standard uint16 unwrap rule.
		delta := int32(h.SequenceNumber) - int32(prevRaw)
		if delta > 32767 {
			delta -= 65536
		} else if delta < -32768 {
			delta += 65536
		}
		ext := prevExt + int64(delta)
		h.ExtendedSeq = uint64(ext)

		switch {
		case delta == 0:
			rep.Duplicates++
			rep.Faults = append(rep.Faults, media.NewFault(
				media.ClassRTPSegmentGap, media.SeverityWarning,
				fmt.Sprintf("duplicate RTP sequence %d at packet %d", h.SequenceNumber, i),
				media.WithScope(media.ScopeSegment),
			))
		case delta < 0:
			rep.Backwards++
			rep.Faults = append(rep.Faults, media.NewFault(
				media.ClassBackwardPTS, media.SeverityError,
				fmt.Sprintf("RTP sequence moved backwards from %d to %d at packet %d", prevRaw, h.SequenceNumber, i),
				media.WithScope(media.ScopeSegment),
			))
		case delta > 1:
			missing := int(delta - 1)
			rep.Gaps += missing
			rep.Faults = append(rep.Faults, media.NewFault(
				media.ClassRTPSegmentGap, media.SeverityWarning,
				fmt.Sprintf("RTP sequence gap of %d before %d at packet %d", missing, h.SequenceNumber, i),
				media.WithScope(media.ScopeSegment),
			))
		}
		if prevRaw == 65535 && h.SequenceNumber == 0 && delta == 1 {
			rep.WrapCount++
		}

		prevRaw = h.SequenceNumber
		prevExt = ext
		rep.LastSeq = h.SequenceNumber
		rep.LastExtSeq = uint64(ext)
	}
	return rep, nil
}

// MustMarshalPacket is a small fixture helper used by tests and demos.
func MustMarshalPacket(seq uint16) []byte {
	p := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      uint32(seq) * 3000,
			SSRC:           0x4d503452,
		},
		Payload: []byte{0x00, 0x00, 0x00, 0x01},
	}
	b, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}
