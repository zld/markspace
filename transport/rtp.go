// Package transport implements the RTP receive side: parsing, extended
// sequence numbers, duplicate filtering, bounded reordering, gap accounting
// and bandwidth-driven receive-window adaptation.
package transport

import (
	"fmt"

	"github.com/pion/rtp"

	"github.com/zld/media-check/media"
)

// Packet is the normalized view of one RTP packet.
type Packet struct {
	Seq         uint16 `json:"seq"`
	ExtSeq      uint64 `json:"ext_seq"`
	Timestamp   uint32 `json:"timestamp"`
	SSRC        uint32 `json:"ssrc"`
	PayloadType uint8  `json:"payload_type"`
	Marker      bool   `json:"marker"`
	Payload     []byte `json:"-"`
	Size        int    `json:"size"`
	Arrival     int64  `json:"arrival_ns"`
}

// Parse decodes an RTP datagram and wraps it with an arrival timestamp.
func Parse(buf []byte, arrival int64) (Packet, error) {
	var pk rtp.Packet
	if err := pk.Unmarshal(buf); err != nil {
		return Packet{}, media.NewError(media.ClassCorruptPacket, "rtp.parse", 0, "unmarshal: %v", err)
	}
	if pk.Version != 2 {
		return Packet{}, media.NewError(media.ClassCorruptPacket, "rtp.parse", 0, "unsupported RTP version %d", pk.Version)
	}
	if len(pk.Payload) == 0 {
		return Packet{}, media.NewError(media.ClassCorruptPacket, "rtp.parse", 0, "empty payload").WithPacket(uint64(pk.SequenceNumber))
	}
	return Packet{
		Seq: pk.SequenceNumber, Timestamp: pk.Timestamp, SSRC: pk.SSRC,
		PayloadType: pk.PayloadType, Marker: pk.Marker,
		Payload: append([]byte(nil), pk.Payload...), Size: len(buf), Arrival: arrival,
	}, nil
}

// Marshal encodes a Packet back into an RTP datagram. It is used to build
// deterministic fixtures and to prove parse/marshal round-trips.
func Marshal(p Packet) ([]byte, error) {
	m := rtp.Packet{
		Header: rtp.Header{
			Version: 2, Marker: p.Marker, PayloadType: p.PayloadType,
			SequenceNumber: p.Seq, Timestamp: p.Timestamp, SSRC: p.SSRC,
		},
		Payload: p.Payload,
	}
	buf, err := m.Marshal()
	if err != nil {
		return nil, media.WrapError(media.ClassCorruptPacket, "rtp.marshal", 0, err, "marshal failed")
	}
	return buf, nil
}

// MaxRTPPayload is the largest payload we accept for a single packet.
const MaxRTPPayload = 1400

// BuildPacket creates a deterministic packet with validated payload size.
func BuildPacket(ssrc uint32, seq uint16, ts uint32, payloadType uint8, payload []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > MaxRTPPayload {
		return nil, media.NewError(media.ClassInvalidArgument, "rtp.build", 0, "payload size %d outside 1..%d", len(payload), MaxRTPPayload)
	}
	return Marshal(Packet{
		Seq: seq, Timestamp: ts, SSRC: ssrc, PayloadType: payloadType,
		Payload: payload, Marker: true,
	})
}

// SeqExtender lifts 16-bit RTP sequence numbers to a 64-bit monotonic space
// across wrap-around. It also tolerates small out-of-order offsets around the
// current cycle.
type SeqExtender struct {
	ssrc    uint32
	set     bool
	last    uint16
	cycle   uint64
	base    uint64 // extended sequence of the first observed packet
	maxSeen uint64
}

// NewSeqExtender returns an extender scoped to one SSRC.
func NewSeqExtender(ssrc uint32) *SeqExtender { return &SeqExtender{ssrc: ssrc} }

// Extend returns the extended sequence number for p. A packet from a different
// SSRC is rejected so streams never mix.
func (se *SeqExtender) Extend(p Packet) (uint64, error) {
	if se.ssrc != 0 && p.SSRC != se.ssrc {
		return 0, media.NewError(media.ClassCorruptPacket, "rtp.seq", 0, "unexpected SSRC %d (want %d)", p.SSRC, se.ssrc).WithPacket(uint64(p.Seq))
	}
	if !se.set {
		// Anchor the first packet in the middle of the very first cycle so a
		// packet from the preceding cycle can still be placed correctly.
		se.set = true
		se.last = p.Seq
		se.cycle = 0x10000
		se.base = se.cycle + uint64(p.Seq)
		se.maxSeen = se.base
		return se.base, nil
	}
	// Compare numerically against the last sequence. A jump larger than half
	// the sequence space in either direction means the 16-bit counter wrapped;
	// smaller jumps (including out-of-order arrivals) stay in this cycle.
	diff := int64(p.Seq) - int64(se.last)
	switch {
	case diff > 0x7fff:
		// numerically ahead but semantically behind: previous cycle
		if se.cycle < 0x10000 {
			return 0, media.NewError(media.ClassCorruptPacket, "rtp.seq", 0, "sequence underflow").WithPacket(uint64(p.Seq))
		}
		return se.cycle - 0x10000 + uint64(p.Seq), nil
	case diff < -0x7fff:
		// numerically behind but semantically ahead: the counter wrapped
		se.cycle += 0x10000
		ext := se.cycle + uint64(p.Seq)
		se.maxSeen = max64(se.maxSeen, ext)
		se.last = p.Seq
		return ext, nil
	default:
		ext := se.cycle + uint64(p.Seq)
		if ext > se.maxSeen {
			se.maxSeen = ext
			se.last = p.Seq
		}
		return ext, nil
	}
}

func max64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

// DedupFilter is a bounded duplicate filter over extended sequence numbers.
// Duplicates inside the window are dropped; entries older than the window are
// treated as stale rather than duplicates to keep memory bounded.
type DedupFilter struct {
	window  uint64
	seen    map[uint64]struct{}
	maxSeen uint64
	started bool
	dropped int
}

// NewDedupFilter builds a filter retaining the most recent window sequences.
func NewDedupFilter(window int) (*DedupFilter, error) {
	if window < 1 || window > 1<<20 {
		return nil, media.NewError(media.ClassInvalidArgument, "rtp.dedup", 0, "window %d outside 1..1048576", window)
	}
	return &DedupFilter{window: uint64(window), seen: make(map[uint64]struct{})}, nil
}

// Seen reports whether ext was already observed and records it otherwise.
// The second return value is true for a duplicate.
func (d *DedupFilter) Seen(ext uint64) bool {
	if !d.started {
		d.started = true
		d.maxSeen = ext
	}
	if _, ok := d.seen[ext]; ok {
		d.dropped++
		return true
	}
	d.seen[ext] = struct{}{}
	if ext > d.maxSeen {
		d.maxSeen = ext
	}
	if d.maxSeen > d.window {
		cut := d.maxSeen - d.window
		for k := range d.seen {
			if k < cut {
				delete(d.seen, k)
			}
		}
	}
	return false
}

// Dropped returns the number of duplicates suppressed.
func (d *DedupFilter) Dropped() int { return d.dropped }

// Reset clears the filter while preserving the configured window.
func (d *DedupFilter) Reset() {
	d.seen = make(map[uint64]struct{})
	d.maxSeen, d.started, d.dropped = 0, false, 0
}

// Stats aggregates RTP receive counters for the manifest.
type Stats struct {
	Received    int `json:"received"`
	Duplicates  int `json:"duplicates"`
	Reordered   int `json:"reordered"`
	LateArrived int `json:"late_arrived"`
	Lost        int `json:"lost"`
	Recovered   int `json:"recovered"`
	Dispatched  int `json:"dispatched"`
}

func (s Stats) String() string {
	return fmt.Sprintf("received=%d dup=%d reordered=%d late=%d lost=%d recovered=%d dispatched=%d",
		s.Received, s.Duplicates, s.Reordered, s.LateArrived, s.Lost, s.Recovered, s.Dispatched)
}
