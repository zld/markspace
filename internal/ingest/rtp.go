// Package ingest parses RTP datagram streams and compressed audio packets
// into the shared integer timeline, never fabricating missing audio.
package ingest

import (
	"sort"

	"github.com/pion/rtp"

	"continuity/internal/timeline"
)

// Packet is a parsed RTP packet with its extended (unwrapped) sequence number.
type Packet struct {
	Seq       uint64 // extended sequence number (16-bit wrap unfolded)
	Timestamp uint32 // RTP timestamp on the media clock
	Payload   []byte
	Offset    int64 // byte offset of the datagram in the source stream
}

// Gap is an inclusive range of extended sequence numbers still missing after
// reorder-window processing.
type Gap struct {
	StartSeq uint64 `json:"startSeq"`
	EndSeq   uint64 `json:"endSeq"`
}

// Result is the outcome of ingesting one RTP stream.
type Result struct {
	Packets    []Packet // sorted by extended seq, duplicates removed
	Gaps       []Gap
	Duplicates int
	Late       int // packets arriving outside the reorder window
	Faults     []*timeline.Fault
}

// IngestRTP parses datagrams in arrival order, unfolds 16-bit sequence
// wraparound, drops duplicates (counted once), applies a reorder window and
// reports sequence numbers still missing as gaps. Truncated datagrams are
// reported as structured faults and excluded; nothing is fabricated.
func IngestRTP(track string, datagrams [][]byte, reorderWindow int) *Result {
	res := &Result{}
	type entry struct {
		pkt   Packet
		seq16 uint16
	}
	var entries []entry
	var off int64
	for _, dg := range datagrams {
		var p rtp.Packet
		if err := p.Unmarshal(dg); err != nil {
			res.Faults = append(res.Faults, &timeline.Fault{
				Track: track, Stage: timeline.StageIngest, Offset: off,
				Msg: "truncated or invalid RTP datagram: " + err.Error(),
			})
			off += int64(len(dg))
			continue
		}
		entries = append(entries, entry{
			pkt: Packet{
				Timestamp: p.Timestamp,
				Payload:   p.Payload,
				Offset:    off,
			},
			seq16: p.SequenceNumber,
		})
		off += int64(len(dg))
	}

	// Unfold 16-bit wraparound in arrival order.
	var cycles uint64
	var prev uint16
	for i := range entries {
		s := entries[i].seq16
		if i > 0 {
			// Forward wrap: sequence jumped from high to low.
			if s < prev && prev-s > 0x8000 {
				cycles++
			} else if s > prev && s-prev > 0x8000 && cycles > 0 {
				// Late packet from before the wrap.
				entries[i].pkt.Seq = (cycles-1)<<16 | uint64(s)
				prev = s
				continue
			}
		}
		entries[i].pkt.Seq = cycles<<16 | uint64(s)
		prev = s
	}

	// Reorder window: count packets that arrive later than the window allows.
	if reorderWindow > 0 {
		var maxSeen uint64
		for i, e := range entries {
			if i == 0 {
				maxSeen = e.pkt.Seq
				continue
			}
			if e.pkt.Seq+uint64(reorderWindow) < maxSeen {
				res.Late++
			}
			if e.pkt.Seq > maxSeen {
				maxSeen = e.pkt.Seq
			}
		}
	}

	// Sort by extended sequence and drop duplicates (counted once).
	sort.Slice(entries, func(i, j int) bool { return entries[i].pkt.Seq < entries[j].pkt.Seq })
	seen := make(map[uint64]bool, len(entries))
	for _, e := range entries {
		if seen[e.pkt.Seq] {
			res.Duplicates++
			continue
		}
		seen[e.pkt.Seq] = true
		res.Packets = append(res.Packets, e.pkt)
	}

	// Anything still missing after reordering is a gap.
	for i := 1; i < len(res.Packets); i++ {
		prev, cur := res.Packets[i-1].Seq, res.Packets[i].Seq
		if cur > prev+1 {
			res.Gaps = append(res.Gaps, Gap{StartSeq: prev + 1, EndSeq: cur - 1})
		}
	}
	return res
}
