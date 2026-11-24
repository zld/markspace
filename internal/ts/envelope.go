package ts

import (
	"github.com/pion/rtp"

	"github.com/example/tsrepair/internal/model"
)

// Envelope is one parsed RTP packet carrying MPEG-TS packets (RFC 2250 style:
// the payload is a whole number of 188-byte TS packets).
type Envelope struct {
	SequenceNumber uint16
	SSRC           uint32
	Timestamp      uint32
	Packets        []*Packet
}

// EncodeRTP wraps raw TS bytes into RTP packets with a fixed starting
// sequence number. Every RTP packet carries whole TS packets only.
func EncodeRTP(tsData []byte, ssrc uint32, rtpTS uint32, startSeq uint16, payloadType uint8) ([][]byte, error) {
	if len(tsData)%PacketSize != 0 {
		return nil, model.New(model.StageParse, model.SeverityFatal,
			"RTP payload source is not a whole number of 188-byte packets").
			Wrap(model.ErrPacketLength)
	}
	seq := rtp.NewFixedSequencer(startSeq)
	var out [][]byte
	for off := 0; off < len(tsData); off += PacketSize {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    payloadType,
				SSRC:           ssrc,
				SequenceNumber: seq.NextSequenceNumber(),
				Timestamp:      rtpTS,
			},
			Payload: append([]byte(nil), tsData[off:off+PacketSize]...),
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, model.New(model.StageParse, model.SeverityInternal,
				"rtp marshal failed").Wrap(err)
		}
		out = append(out, raw)
	}
	return out, nil
}

// LooksLikeRTP applies the agreed encapsulation test: a valid 12-byte RTP
// header (version 2) whose payload is a whole number of TS packets.
func LooksLikeRTP(buf []byte) bool {
	if len(buf) < 12 || buf[0]>>6 != 2 {
		return false
	}
	var p rtp.Packet
	if err := p.Unmarshal(buf); err != nil {
		return false
	}
	return len(p.Payload)%PacketSize == 0
}

// ParseRTP parses one RTP datagram and every TS packet in its payload.
func ParseRTP(buf []byte) (*Envelope, error) {
	if len(buf) < 12 || buf[0]>>6 != 2 {
		return nil, model.New(model.StageParse, model.SeverityFatal,
			"buffer is neither a TS packet nor a valid RTP packet").
			Wrap(model.ErrUnknownEncap)
	}
	var r rtp.Packet
	if err := r.Unmarshal(buf); err != nil {
		return nil, model.New(model.StageParse, model.SeverityFatal,
			"invalid RTP header").Wrap(model.ErrUnknownEncap).Wrap(err)
	}
	if len(r.Payload)%PacketSize != 0 || len(r.Payload) == 0 {
		return nil, model.New(model.StageParse, model.SeverityFatal,
			"RTP payload is not a whole number of 188-byte TS packets").
			Wrap(model.ErrPacketLength).At(model.PIDNotApplicable, int64(12))
	}
	env := &Envelope{
		SequenceNumber: r.SequenceNumber,
		SSRC:           r.SSRC,
		Timestamp:      r.Timestamp,
	}
	base := int64(12) + int64(r.Header.MarshalSize()-12)
	for off := 0; off < len(r.Payload); off += PacketSize {
		pkt, err := Parse(r.Payload[off:off+PacketSize], base+int64(off))
		if err != nil {
			return nil, err
		}
		env.Packets = append(env.Packets, pkt)
	}
	return env, nil
}

// RTPGap compares two consecutive RTP sequence numbers. Returns the number
// of missing datagrams in [0, 65535]; 0 means consecutive or duplicate.
func RTPGap(prev, cur uint16) uint16 {
	return cur - prev - 1
}
