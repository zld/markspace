// Package ingest defines the packet event model and the reordering stage
// that turns a lossy, reordered, duplicated RTP/RTCP stream into a
// committed, gap-annotated packet sequence.
package ingest

import (
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// Kind classifies a packet event.
type Kind uint8

const (
	KindRTP Kind = iota + 1
	KindRTCP
)

// Event is one observed packet on the wire. Arrival is a monotonically
// increasing observation index assigned by the capture/generator. Ext is the
// extended sequence number assigned by the reorderer when the packet is
// committed; it is zero for RTCP events and for uncommitted packets.
type Event struct {
	Kind    Kind
	Arrival int64
	Ext     int64

	// RTP fields (KindRTP)
	Packet *rtp.Packet

	// RTCP fields (KindRTCP): sender report if present
	SR *rtcp.SenderReport
}

// Seq returns the raw 16-bit RTP sequence number (RTP only).
func (e Event) Seq() uint16 {
	if e.Packet == nil {
		return 0
	}
	return e.Packet.SequenceNumber
}

// SeqExtender maps 16-bit RTP sequence numbers onto a signed extended domain.
// Each new number is placed within half a sequence space of the highest
// extended value seen so far, so wrap-around, reordering and duplicated
// numbers all resolve without unsigned underflow. The domain may legitimately
// contain negative values when a pre-wrap packet arrives after the first
// post-wrap packet.
type SeqExtender struct {
	origin int64
	last   int64
	init   bool
}

// Origin returns the extended value assigned to the first packet, which is
// the lowest position a reorderer may need to commit from.
func (e *SeqExtender) Origin() int64 { return e.origin }

// Extend returns the extended sequence number for a raw 16-bit value.
func (e *SeqExtender) Extend(seq uint16) int64 {
	if !e.init {
		e.init = true
		e.origin = int64(seq)
		e.last = e.origin
		return e.origin
	}
	// difference in the 16-bit space, reinterpreted as a signed offset
	diff := int64(int16(seq - uint16(e.last)))
	cand := e.last + diff
	if cand > e.last {
		e.last = cand
	}
	return cand
}

// Max returns the highest extended sequence number observed so far.
func (e *SeqExtender) Max() int64 { return e.last }

// Gap marks a committed range of missing extended sequence numbers
// [From, To] inclusive.
type Gap struct {
	From int64
	To   int64
}

// Stats summarizes reordering outcomes.
type Stats struct {
	Received   int   // RTP packets observed on the wire
	Duplicates int   // exact re-observations of an already-seen ext seq
	Late       int   // arrived after their position was committed
	Lost       int   // sequence numbers never observed
	Gaps       []Gap // committed loss ranges
}
