// Package rtpingest accepts RTP-packetized auxiliary input. It uses
// pion/rtp to parse packet headers and timestamps, then extracts codec payload
// through the matching depacketizer so the rest of the pipeline receives the
// same raw access units regardless of transport framing.
package rtpingest

import (
	"context"
	"errors"
	"fmt"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"

	"mediad/internal/ingest"
	"mediad/internal/model"
)

// depacketizer is the pion depacketizer surface this package relies on.
// AV1Packet in pion/rtp v1.8.11 only exposes Unmarshal, so the contract is
// deliberately limited to that method.
type depacketizer interface {
	Unmarshal(packet []byte) ([]byte, error)
}

// h265Adapter normalizes the pion H265Packet API, whose Unmarshal stores the
// decoded unit internally and returns nil, into the same Unmarshal contract
// used by the other codecs, emitting an Annex-B framed NAL unit.
type h265Adapter struct{ inner *codecs.H265Packet }

func (a *h265Adapter) Unmarshal(packet []byte) ([]byte, error) {
	if _, err := a.inner.Unmarshal(packet); err != nil {
		return nil, err
	}
	switch p := a.inner.Packet().(type) {
	case *codecs.H265SingleNALUnitPacket:
		hdr := uint16(p.PayloadHeader())
		out := make([]byte, 0, 4+2+len(p.Payload()))
		out = append(out, 0, 0, 0, 1)
		out = append(out, byte(hdr>>8), byte(hdr))
		out = append(out, p.Payload()...)
		return out, nil
	default:
		// Fragmentation/aggregation are not produced by the fixture; reject
		// rather than silently dropping units.
		return nil, model.New(model.ErrTruncatedUnit, ingest.CodecH265, -1,
			"unsupported H.265 RTP packetization mode %T", p)
	}
}

// newDepacketizer returns the pion depacketizer for a supported codec.
func newDepacketizer(codec string) (depacketizer, error) {
	switch codec {
	case ingest.CodecH264:
		return &codecs.H264Packet{}, nil
	case ingest.CodecH265:
		return &h265Adapter{inner: &codecs.H265Packet{}}, nil
	case ingest.CodecAV1:
		return &codecs.AV1Packet{}, nil
	default:
		return nil, model.New(model.ErrUnknownCodec, codec, -1, "no RTP depacketizer for codec")
	}
}

// TimedPacket is one parsed RTP packet with its transport metadata.
type TimedPacket struct {
	SequenceNumber uint16
	Timestamp      uint32
	Marker         bool
	PayloadType    uint8
	SSRC           uint32
	Raw            []byte
}

// Parse decodes one RTP datagram, validating the version field, using
// pion/rtp's parser rather than hand-rolled bit shifts.
func Parse(datagram []byte) (*TimedPacket, error) {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(datagram); err != nil {
		return nil, model.New(model.ErrIllegalHeader, "", -1, "RTP parse failed: %v", err)
	}
	if pkt.Version != 2 {
		return nil, model.New(model.ErrIllegalHeader, "", -1, "RTP version must be 2, got %d", pkt.Version)
	}
	return &TimedPacket{
		SequenceNumber: pkt.SequenceNumber,
		Timestamp:      pkt.Timestamp,
		Marker:         pkt.Marker,
		PayloadType:    pkt.PayloadType,
		SSRC:           pkt.SSRC,
		Raw:            append([]byte(nil), datagram...),
	}, nil
}

// AccessUnitBuilder reassembles codec access units from a stream of RTP
// packets. Marker-bounded packets are treated as unit boundaries; the pion
// depacketizer strips transport framing and returns the codec payload.
type AccessUnitBuilder struct {
	codec      string
	dep        depacketizer
	assembled  []byte
	firstTS    uint32
	haveTS     bool
	unitCount  int
	lastSeq    uint16
	seqChecked bool
}

// NewAccessUnitBuilder builds a builder for the given codec.
func NewAccessUnitBuilder(codec string) (*AccessUnitBuilder, error) {
	dep, err := newDepacketizer(codec)
	if err != nil {
		return nil, err
	}
	return &AccessUnitBuilder{codec: codec, dep: dep}, nil
}

// Push feeds one parsed RTP packet. When the packet's marker closes an access
// unit, the reassembled codec payload and its RTP timestamp are returned;
// otherwise ok is false and more packets are expected.
func (b *AccessUnitBuilder) Push(p *TimedPacket) (payload []byte, rtpTS uint32, ok bool, err error) {
	// Sequence continuity check (mod 65536).
	if b.seqChecked {
		if p.SequenceNumber != b.lastSeq+1 {
			return nil, 0, false, model.New(model.ErrTruncatedUnit, b.codec, -1,
				"RTP sequence gap: expected %d got %d", b.lastSeq+1, p.SequenceNumber)
		}
	}
	b.lastSeq = p.SequenceNumber
	b.seqChecked = true
	if !b.haveTS {
		b.firstTS = p.Timestamp
		b.haveTS = true
	}

	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(p.Raw); err != nil {
		return nil, 0, false, model.New(model.ErrIllegalHeader, b.codec, -1, "RTP unmarshal: %v", err)
	}
	nal, err := b.dep.Unmarshal(pkt.Payload)
	if err != nil {
		return nil, 0, false, model.New(model.ErrTruncatedUnit, b.codec, -1, "depacketize: %v", err)
	}
	b.assembled = append(b.assembled, nal...)

	if p.Marker {
		out := b.assembled
		ts := p.Timestamp
		b.assembled = nil
		b.unitCount++
		return out, ts, true, nil
	}
	return nil, 0, false, nil
}

// UnitCount returns the number of complete access units emitted so far.
func (b *AccessUnitBuilder) UnitCount() int { return b.unitCount }

// FirstTimestamp returns the RTP timestamp of the first packet seen.
func (b *AccessUnitBuilder) FirstTimestamp() (uint32, error) {
	if !b.haveTS {
		return 0, errors.New("no packets seen")
	}
	return b.firstTS, nil
}

// RTPToNS converts an RTP timestamp delta to integer nanoseconds given the
// RTP clock rate in Hz, using a single integer rounding step.
func RTPToNS(timestampDelta uint64, clockRateHz uint32) (int64, error) {
	if clockRateHz == 0 {
		return 0, model.New(model.ErrZeroTimebase, "", -1, "RTP clock rate must not be zero")
	}
	scaled, err := model.CheckedMul(int64(timestampDelta), model.NanoPerSec)
	if err != nil {
		return 0, err
	}
	return model.RoundDiv(scaled, int64(clockRateHz))
}

// String renders a timed packet for debugging.
func (p *TimedPacket) String() string {
	return fmt.Sprintf("rtp(seq=%d ts=%d marker=%v pt=%d)", p.SequenceNumber, p.Timestamp, p.Marker, p.PayloadType)
}

// Drain is a context-aware helper that processes a channel of datagrams and
// returns every completed access unit in order.
func Drain(ctx context.Context, codec string, in <-chan []byte) ([][]byte, error) {
	b, err := NewAccessUnitBuilder(codec)
	if err != nil {
		return nil, err
	}
	var units [][]byte
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case datagram, more := <-in:
			if !more {
				return units, nil
			}
			tp, err := Parse(datagram)
			if err != nil {
				return nil, err
			}
			if payload, _, done, err := b.Push(tp); err != nil {
				return nil, err
			} else if done {
				units = append(units, payload)
			}
		}
	}
}
