// Package rtptrace associates Matroska block payloads with RTP sequence
// numbers and timestamps using pion/rtp, and derives loss and jitter
// statistics per track without reordering the source blocks.
package rtptrace

import (
	"github.com/pion/rtp"

	"github.com/local/tracecheck/internal/clock"
	"github.com/local/tracecheck/internal/mkv"
)

// Event links one parsed RTP packet to its source block.
type Event struct {
	Sequence     uint16
	Timestamp    uint32 // RTP clock units, as received
	SSRC         uint32
	PayloadType  uint8
	Marker       bool
	TrackNumber  uint64
	SourceOffset int64
	AbsTime      clock.Tick
	PayloadLen   int
}

// LossRange describes a contiguous run of missing sequence numbers.
type LossRange struct {
	TrackNumber uint64
	Start       uint16
	End         uint16 // inclusive; equals Start for a single missing packet
	Count       int
}

// Stats summarizes one track's RTP trace.
type Stats struct {
	TrackNumber uint64
	PacketCount int
	LossRanges  []LossRange
	LostPackets int
	// JitterNS is the inter-packet arrival jitter expressed on the shared
	// nanosecond time base (RFC 3550 algorithm, absolute differences).
	JitterNS    clock.Tick
	MaxJitterNS clock.Tick
}

// Trace holds every decoded event plus per-track statistics.
type Trace struct {
	Events []*Event
	Stats  map[uint64]*Stats
	// DecodeErrors records packets whose payload was not a valid RTP packet,
	// keyed by source offset so failures remain locatable.
	DecodeErrors []DecodeError
}

// DecodeError records an unparseable RTP payload with its location.
type DecodeError struct {
	SourceOffset int64
	TrackNumber  uint64
	Reason       string
}

// FromBlocks decodes RTP packets carried in block payloads. Packets are
// kept in source arrival order; statistics handle sequence wrapping.
func FromBlocks(blocks []*mkv.Block) *Trace {
	tr := &Trace{Stats: map[uint64]*Stats{}}
	type prev struct {
		seq      uint16
		haveSeq  bool
		arrival  clock.Tick
		haveTime bool
	}
	last := map[uint64]*prev{}

	for _, blk := range blocks {
		st := tr.Stats[blk.TrackNumber]
		if st == nil {
			st = &Stats{TrackNumber: blk.TrackNumber}
			tr.Stats[blk.TrackNumber] = st
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(blk.Payload); err != nil {
			tr.DecodeErrors = append(tr.DecodeErrors, DecodeError{
				SourceOffset: blk.SourceOffset,
				TrackNumber:  blk.TrackNumber,
				Reason:       err.Error(),
			})
			continue
		}
		st.PacketCount++
		tr.Events = append(tr.Events, &Event{
			Sequence:     pkt.SequenceNumber,
			Timestamp:    pkt.Timestamp,
			SSRC:         pkt.SSRC,
			PayloadType:  pkt.PayloadType,
			Marker:       pkt.Marker,
			TrackNumber:  blk.TrackNumber,
			SourceOffset: blk.SourceOffset,
			AbsTime:      blk.AbsTime,
			PayloadLen:   len(pkt.Payload),
		})

		p := last[blk.TrackNumber]
		if p == nil {
			p = &prev{}
			last[blk.TrackNumber] = p
		}
		if p.haveSeq {
			gap := int(pkt.SequenceNumber) - int(p.seq)
			if gap < 0 {
				gap += 1 << 16 // handle 16-bit wrap
			}
			if gap > 1 {
				lost := gap - 1
				st.LossRanges = append(st.LossRanges, LossRange{
					TrackNumber: blk.TrackNumber,
					Start:       p.seq + 1,
					End:         pkt.SequenceNumber - 1,
					Count:       lost,
				})
				st.LostPackets += lost
			}
		}
		if p.haveTime {
			d := clock.Abs(blk.AbsTime, p.arrival)
			// RFC 3550 smoothed jitter J += (|D| - J)/16. Integer division
			// would truncate a negative term; take the signed term then
			// clamp so the accumulated jitter stays non-negative.
			term := (d - st.JitterNS) / 16
			st.JitterNS += term
			if st.JitterNS < 0 {
				st.JitterNS = 0
			}
			if d > st.MaxJitterNS {
				st.MaxJitterNS = d
			}
		}
		p.seq = pkt.SequenceNumber
		p.haveSeq = true
		p.arrival = blk.AbsTime
		p.haveTime = true
	}
	return tr
}

// TotalLost sums lost packets across all tracks.
func (tr *Trace) TotalLost() int {
	n := 0
	for _, st := range tr.Stats {
		n += st.LostPackets
	}
	return n
}
