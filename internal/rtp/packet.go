// Package rtp parses RTP packets with pion/rtp and extracts packet events
// onto the unified clock.Timeline. Parse failures keep the RTP sequence
// number inside the shared structured error type.
package rtp

import (
	"github.com/pion/rtp"

	"mediakernel/internal/clock"
)

// Event is one mapped RTP packet: wire fields plus its integer-nanosecond
// media time and the PCM payload bytes for index/lookup processing.
type Event struct {
	SeqNum     uint16
	SSRC       uint32
	Timestamp  uint32
	MediaNanos int64
	Marker     bool
	Payload    []byte
}

// Extractor binds an SSRC/clock-rate pair to a timeline and turns raw RTP
// datagrams into timeline-mapped events.
type Extractor struct {
	timeline  *clock.Timeline
	ssrc      uint32
	clockRate uint32
}

// NewExtractor registers ssrc at clockRate on the shared timeline.
func NewExtractor(tl *clock.Timeline, ssrc uint32, clockRate uint32) (*Extractor, error) {
	if err := tl.RegisterStream(ssrc, clockRate); err != nil {
		return nil, err
	}
	return &Extractor{timeline: tl, ssrc: ssrc, clockRate: clockRate}, nil
}

// Parse unmarshals one datagram, verifies version/SSRC, and maps its RTP
// timestamp onto the timeline. Wire-level failures are wrapped so the
// sequence number remains available whenever the header is readable.
func (e *Extractor) Parse(datagram []byte) (*Event, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(datagram); err != nil {
		// Best-effort sequence recovery from the fixed wire header (bytes 2-3).
		var seq uint16
		hasSeq := len(datagram) >= 4
		if hasSeq {
			seq = uint16(datagram[2])<<8 | uint16(datagram[3])
		}
		ce := clock.NewError("rtp", clock.CatBadLength,
			"rtp unmarshal failed").Wrap(err)
		if hasSeq {
			ce.WithSeq(seq)
		}
		return nil, ce
	}
	if pkt.Version != 2 {
		return nil, clock.NewError("rtp", clock.CatInvalidParam,
			"unsupported RTP version %d", pkt.Version).WithSeq(pkt.SequenceNumber)
	}
	if pkt.SSRC != e.ssrc {
		return nil, clock.NewError("rtp", clock.CatLayoutMismatch,
			"ssrc=%d does not match registered ssrc=%d", pkt.SSRC, e.ssrc).
			WithSeq(pkt.SequenceNumber)
	}
	nanos, err := e.timeline.ObserveRTP(pkt.SSRC, pkt.Timestamp, pkt.SequenceNumber)
	if err != nil {
		return nil, err
	}
	return &Event{
		SeqNum:     pkt.SequenceNumber,
		SSRC:       pkt.SSRC,
		Timestamp:  pkt.Timestamp,
		MediaNanos: nanos,
		Marker:     pkt.Marker,
		Payload:    append([]byte(nil), pkt.Payload...),
	}, nil
}

// ParseAll parses a datagram slice in arrival order and returns every
// mapped event. The first failure aborts and keeps its sequence locator.
func (e *Extractor) ParseAll(datagrams [][]byte) ([]*Event, error) {
	events := make([]*Event, 0, len(datagrams))
	for _, d := range datagrams {
		ev, err := e.Parse(d)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, nil
}
