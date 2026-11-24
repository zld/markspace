// Package ingest receives RTP packets and normalizes packet sequence order.
package ingest

import (
	"bytes"
	"sort"

	"github.com/pion/rtp"

	"example.com/media-kernel/media"
)

// StreamID identifies a local generated or supplied RTP stream.
type StreamID string

// Packet is a successfully parsed and sequence-normalized RTP packet.
type Packet struct {
	Stream      StreamID
	Raw         []byte
	Header      rtp.Header
	Payload     []byte
	ExtSequence uint64
	Arrival     int
}

// Result is the outcome of accepting one wire packet.
type Result struct {
	Packet         *Packet
	Classification Classification
	ParseError     *media.Error
}

// Collector owns one sequence tracker and stores non-duplicate packets.
type Collector struct {
	Stream  StreamID
	Tracker *Tracker
	packets map[uint64]*Packet
	arrival int
}

// NewCollector constructs a collector for stream id.
func NewCollector(id StreamID) *Collector {
	return &Collector{Stream: id, Tracker: NewTracker(), packets: make(map[uint64]*Packet)}
}

// ParseRTP unmarshals one complete RTP datagram with pion/rtp.
func ParseRTP(raw []byte) (*rtp.Packet, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return nil, media.NewError("ingest.rtp", media.CodeRTPInvalid,
			"RTP header or payload is invalid", err).WithOffset(0)
	}
	return &pkt, nil
}

// BuildRTP is a test/fixture helper that marshals one RTP packet.
func BuildRTP(seq uint16, timestamp uint32, payload []byte, marker bool) ([]byte, error) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      timestamp,
			SSRC:           0x50313630, // "P160"
			Marker:         marker,
		},
		Payload: bytes.Clone(payload),
	}
	raw, err := pkt.Marshal()
	if err != nil {
		return nil, media.NewError("ingest.rtp", media.CodeRTPInvalid, "failed to marshal RTP fixture", err)
	}
	return raw, nil
}

// Ingest parses and classifies one raw RTP datagram. Duplicates are counted and
// returned as results but are not retained or timed a second time.
func (c *Collector) Ingest(raw []byte) Result {
	parsed, err := ParseRTP(raw)
	if err != nil {
		return Result{ParseError: err.(*media.Error)}
	}

	// Detect duplicate before Extend records the sequence.
	class := c.Tracker.Classify(parsed.SequenceNumber)
	c.arrival++
	if class.Disposition == DispositionDuplicate {
		return Result{Classification: class}
	}

	pkt := &Packet{
		Stream:      c.Stream,
		Raw:         bytes.Clone(raw),
		Header:      parsed.Header,
		Payload:     bytes.Clone(parsed.Payload),
		ExtSequence: class.Extended,
		Arrival:     c.arrival,
	}
	c.packets[class.Extended] = pkt
	return Result{Packet: pkt, Classification: class}
}

// Ordered returns retained packets sorted by extended sequence number.
func (c *Collector) Ordered() []*Packet {
	seqs := make([]uint64, 0, len(c.packets))
	for seq := range c.packets {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	out := make([]*Packet, 0, len(seqs))
	for _, seq := range seqs {
		out = append(out, c.packets[seq])
	}
	return out
}

// Missing returns extended sequence numbers not yet received.
func (c *Collector) Missing() []uint64 { return c.Tracker.Missing() }

// Gaps coalesces missing sequence numbers.
func (c *Collector) Gaps() []Gap { return c.Tracker.Gaps() }

// DuplicateCount returns duplicate observations.
func (c *Collector) DuplicateCount() int { return c.Tracker.Duplicates }
