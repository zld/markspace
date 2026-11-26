// Package rtp provides sequence extension, bounded jitter reordering, and
// explicit repair/gap evidence for RTP streams.
package rtp

import (
	"fmt"

	pionrtp "github.com/pion/rtp"

	"github.com/zld/medcheck/pkg/media"
)

const seqMod = uint64(1) << 16

// Packet is a reordered RTP packet with its unwrapped sequence and provenance.
type Packet struct {
	RTP        *pionrtp.Packet `json:"-"`
	Raw        []byte          `json:"-"`
	Sequence   uint16          `json:"sequence"`
	ExtSeq     uint64          `json:"ext_sequence"`
	Timestamp  uint32          `json:"rtp_timestamp"`
	ArrivalUS  int64           `json:"arrival_us"`
	ByteOffset int64           `json:"byte_offset"`
	Recovered  bool            `json:"recovered"`
}

// Gap is a sequence interval that could not be filled before its deadline.
type Gap struct {
	ExtSeq   uint64         `json:"ext_sequence"`
	Sequence uint16         `json:"sequence"`
	Reason   media.Category `json:"reason"`
}

// Stats contains quantitative buffer evidence.
type Stats struct {
	Received   int `json:"received"`
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Late       int `json:"late_outside_window"`
	Outside    int `json:"outside_forward_window"`
	Repaired   int `json:"recovered"`
	Gaps       int `json:"unrecoverable_gaps"`
	Wraps      int `json:"sequence_wraps"`
}

// Ack reports how one input packet was treated.
type Ack struct {
	Disposition media.Category `json:"disposition"`
	ExtSeq      uint64         `json:"ext_sequence"`
	Sequence    uint16         `json:"sequence"`
}

// Buffer is a bounded extended-sequence reorder buffer.
type Buffer struct {
	TrackID string
	Window  uint64

	next    uint64
	maxSeen uint64
	lastRaw uint16
	hasLast bool
	started bool
	flushed bool
	pending map[uint64]*Packet
	seen    map[uint64]bool
	Wraps   []string
	Issues  media.Issues
	Stats   Stats
}

// NewJitterBuffer creates a buffer with a positive packet-window size.
func NewJitterBuffer(trackID string, window int) (*Buffer, error) {
	if window <= 0 {
		return nil, media.NewError(media.CatInvalidArgument, trackID, -1, -1, "jitter window must be positive, got %d", window)
	}
	return &Buffer{
		TrackID: trackID, Window: uint64(window),
		pending: map[uint64]*Packet{}, seen: map[uint64]bool{},
	}, nil
}

// ParsePacket decodes one RTP packet using github.com/pion/rtp.
func ParsePacket(raw []byte) (*pionrtp.Packet, error) {
	var p pionrtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		return nil, media.NewError(media.CatInvalidEncoding, "rtp", -1, -1, "decode RTP packet: %v", err)
	}
	return &p, nil
}

// PushRaw parses and inserts a raw RTP packet.
func (b *Buffer) PushRaw(raw []byte, arrivalUS, byteOffset int64) (*Ack, error) {
	p, err := ParsePacket(raw)
	if err != nil {
		if me, ok := err.(*media.Error); ok {
			me.TrackID = b.TrackID
			me.Offset = byteOffset
			b.Issues = append(b.Issues, me)
		}
		return nil, err
	}
	return b.Push(&Packet{
		RTP: p, Raw: append([]byte(nil), raw...), Sequence: p.SequenceNumber,
		Timestamp: p.Timestamp, ArrivalUS: arrivalUS, ByteOffset: byteOffset,
	})
}

// Extend unwraps a 16-bit sequence relative to buffer state and records wraps.
func (b *Buffer) Extend(seq uint16) uint64 {
	if !b.started {
		b.started = true
		b.lastRaw = seq
		b.next = uint64(seq)
		b.maxSeen = b.next
		b.hasLast = true
		return b.next
	}
	last := b.maxSeen
	ext := unwrapSequence(seq, last)
	if b.hasLast && int32(seq)-int32(b.lastRaw) < -32768 {
		b.Stats.Wraps++
		b.Wraps = append(b.Wraps, fmt.Sprintf("%d->%d", b.lastRaw, seq))
	}
	b.lastRaw = seq
	b.hasLast = true
	return ext
}

func unwrapSequence(seq uint16, last uint64) uint64 {
	candidate := (last &^ (seqMod - 1)) | uint64(seq)
	if candidate+32768 < last {
		candidate += seqMod
	}
	if last >= 32768 && candidate > last+32768 {
		candidate -= seqMod
	}
	return candidate
}

// Push inserts an already decoded packet according to its extended sequence.
func (b *Buffer) Push(pkt *Packet) (*Ack, error) {
	if pkt == nil || pkt.RTP == nil {
		return nil, media.NewError(media.CatInvalidArgument, b.TrackID, -1, -1, "nil RTP packet")
	}
	b.Stats.Received++
	pkt.Sequence = pkt.RTP.SequenceNumber
	pkt.Timestamp = pkt.RTP.Timestamp
	pkt.ExtSeq = b.Extend(pkt.Sequence)
	ack := &Ack{ExtSeq: pkt.ExtSeq, Sequence: pkt.Sequence}

	if b.seen[pkt.ExtSeq] || b.pending[pkt.ExtSeq] != nil {
		b.Stats.Duplicates++
		ack.Disposition = media.CatDuplicatePacket
		b.Issues.Add(media.CatDuplicatePacket, b.TrackID, int64(pkt.ExtSeq), pkt.ByteOffset,
			"duplicate RTP sequence %d", pkt.Sequence)
		return ack, nil
	}
	if b.flushed || pkt.ExtSeq < b.next {
		b.Stats.Late++
		ack.Disposition = media.CatLateWindow
		b.Issues.Add(media.CatLateWindow, b.TrackID, int64(pkt.ExtSeq), pkt.ByteOffset,
			"late RTP sequence %d arrived after window/base %d", pkt.Sequence, b.next)
		return ack, nil
	}
	if pkt.ExtSeq >= b.next+b.Window {
		b.Stats.Outside++
		ack.Disposition = media.CatOutsideWindow
		b.Issues.Add(media.CatOutsideWindow, b.TrackID, int64(pkt.ExtSeq), pkt.ByteOffset,
			"sequence %d is outside bounded window base=%d width=%d", pkt.Sequence, b.next, b.Window)
		return ack, nil
	}

	b.pending[pkt.ExtSeq] = pkt
	b.seen[pkt.ExtSeq] = true
	b.Stats.Accepted++
	if pkt.ExtSeq > b.maxSeen {
		b.maxSeen = pkt.ExtSeq
	}
	ack.Disposition = "accepted"
	return ack, nil
}

// DrainReady releases contiguous packets beginning at next expected sequence.
func (b *Buffer) DrainReady() []*Packet {
	var out []*Packet
	for {
		p, ok := b.pending[b.next]
		if !ok {
			break
		}
		out = append(out, p)
		delete(b.pending, b.next)
		b.next++
	}
	b.pruneSeen()
	return out
}

func (b *Buffer) pruneSeen() {
	cutoff := int64(b.next) - int64(b.Window)
	if cutoff <= 0 {
		return
	}
	for seq := range b.seen {
		if int64(seq) < cutoff {
			delete(b.seen, seq)
		}
	}
}

// Flush expires every missing sequence through maxSeen, records each as an
// unrecoverable gap, and returns all available packets in sequence order.
// No placeholder media payload is manufactured for gaps.
func (b *Buffer) Flush() *Output {
	out := &Output{}
	if !b.started {
		return out
	}
	b.flushed = true
	for seq := b.next; seq <= b.maxSeen; seq++ {
		b.next = seq + 1
		if p, ok := b.pending[seq]; ok {
			out.Packets = append(out.Packets, p)
			delete(b.pending, seq)
			continue
		}
		gap := Gap{ExtSeq: seq, Sequence: uint16(seq), Reason: media.CatUnrecoverableGap}
		out.Gaps = append(out.Gaps, gap)
		b.Stats.Gaps++
		b.Issues.Add(media.CatUnrecoverableGap, b.TrackID, int64(seq), -1,
			"sequence %d missing after repair deadline", uint16(seq))
	}
	// Any packet buffered beyond maxSeen cannot exist; sort map defensively.
	for seq, p := range b.pending {
		out.Packets = append(out.Packets, p)
		delete(b.pending, seq)
	}
	sortPackets(out.Packets)
	out.Issues = b.Issues
	out.Stats = b.Stats
	return out
}

// Output is the ordered result of flushing a buffer.
type Output struct {
	Packets []*Packet
	Gaps    []Gap
	Issues  media.Issues
	Stats   Stats
}

func sortPackets(pkts []*Packet) {
	for i := 1; i < len(pkts); i++ {
		for j := i; j > 0 && pkts[j-1].ExtSeq > pkts[j].ExtSeq; j-- {
			pkts[j-1], pkts[j] = pkts[j], pkts[j-1]
		}
	}
}
