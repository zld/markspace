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
// increasing observation index assigned by the capture/generator.
type Event struct {
	Kind    Kind
	Arrival int64

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

// Gap marks a committed range of missing extended sequence numbers
// [From, To] inclusive.
type Gap struct {
	From uint64
	To   uint64
}

// Stats summarizes reordering outcomes.
type Stats struct {
	Received   int   // RTP packets observed on the wire
	Duplicates int   // exact re-observations of an already-seen ext seq
	Late       int   // arrived after their position was committed
	Lost       int   // sequence numbers never observed
	Gaps       []Gap // committed loss ranges
}
