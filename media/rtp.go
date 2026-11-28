package media

import (
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"mediad/timeline"
)

// RTPPacket is one received packet together with the arrival ordinal, so
// diagnostics can distinguish the order packets arrived in from the order the
// stream intends.
type RTPPacket struct {
	Packet  *rtp.Packet
	Arrival int
	// SrcOffset is the byte offset the packet was read from, for located
	// reporting; zero when the source has no byte layout (synthetic input).
	SrcOffset int64
}

// RTPRecovery is the outcome of reordering and loss accounting for one stream.
type RTPRecovery struct {
	StreamSSRC uint32
	// Packets is the recovered stream ordered by extended sequence number.
	// Each entry keeps its extended sequence so frame assembly can detect gaps.
	Packets []RTPPacketExt
	// Received counts distinct received sequence numbers (duplicates excluded).
	Received int
	// Duplicates counts packets whose sequence number had already arrived.
	Duplicates int
	// Reordered counts packets that arrived after a higher sequence number.
	Reordered int
	// Discarded counts packets too old to belong to the live stream; they are
	// not duplicates and not counted as received.
	Discarded int
	// Expected is the number of sequence numbers between the first and last
	// received packet, inclusive.
	Expected int
	// Unrecoverable counts sequence numbers never received within the stream.
	Unrecoverable int
	// Issues records each unrecoverable gap once.
	Issues timeline.Issues
}

// RTPPacketExt pairs a received packet with its 64-bit extended sequence
// number (the 16-bit field continued across 65536 wraps).
type RTPPacketExt struct {
	Seq     int64
	Pkt     *rtp.Packet
	Arrival int
}

// LossRecoveryRate is the fraction of the expected sequence span that was
// delivered. It is 1.0 for a lossless or perfectly reordered stream.
func (r RTPRecovery) LossRecoveryRate() float64 {
	if r.Expected <= 0 {
		return 1
	}
	return float64(r.Expected-r.Unrecoverable) / float64(r.Expected)
}

// DefaultReorderWindow is how far past the highest received sequence a late
// packet may belong and still be accepted. Packets older than the window are
// treated as unrecoverable even if they eventually arrive, matching RTP's
// bounded reordering tolerance.
const DefaultReorderWindow = 64

// RecoverRTP reorders packets by 16-bit RTP sequence number, counts duplicates
// once, and accounts for gaps. Sequence numbers are extended across the 65536
// wrap using arrival order. A late packet is accepted when it falls within
// window behind the running highest sequence; one that arrives outside the
// window is dropped and the gap it filled stays unrecoverable.
//
// Missing sequence numbers are never fabricated into the packet list: callers
// see a shorter list plus an issue that names the gap, and decide downstream
// whether the affected frame may be published.
func RecoverRTP(packets []RTPPacket) RTPRecovery {
	var out RTPRecovery
	if len(packets) == 0 {
		return out
	}
	return recoverRTPWithWindow(packets, DefaultReorderWindow)
}

func recoverRTPWithWindow(packets []RTPPacket, window int64) RTPRecovery {
	var out RTPRecovery
	out.StreamSSRC = packets[0].Packet.SSRC

	arrival := append([]RTPPacket(nil), packets...)
	sort.SliceStable(arrival, func(i, j int) bool { return arrival[i].Arrival < arrival[j].Arrival })

	seen := map[int64]bool{}
	var highest int64 = -1
	var highestArrivalSeq int64 = -1
	accepted := make([]RTPPacketExt, 0, len(arrival))
	for _, p := range arrival {
		ext := extendSeq(uint16(p.Packet.SequenceNumber), highest)
		if ext > highest {
			highest = ext
		}
		if seen[ext] {
			out.Duplicates++
			continue
		}
		if highestArrivalSeq >= 0 && ext < highestArrivalSeq-window {
			// Too old to belong to the live stream; the gap it would have
			// filled is already accounted as unrecoverable.
			out.Discarded++
			continue
		}
		seen[ext] = true
		if highestArrivalSeq >= 0 && ext < highestArrivalSeq {
			out.Reordered++
		}
		highestArrivalSeq = max64(highestArrivalSeq, ext)
		accepted = append(accepted, RTPPacketExt{Seq: ext, Pkt: p.Packet, Arrival: p.Arrival})
	}
	sort.SliceStable(accepted, func(i, j int) bool { return accepted[i].Seq < accepted[j].Seq })
	out.Packets = accepted
	out.Received = len(accepted)
	if len(accepted) == 0 {
		return out
	}

	first := accepted[0].Seq
	last := accepted[len(accepted)-1].Seq
	out.Expected = int(last-first) + 1
	prev := first - 1
	for _, a := range accepted {
		if a.Seq > prev+1 {
			gap := a.Seq - prev - 1
			out.Unrecoverable += int(gap)
			out.Issues.Add(timeline.Issue{
				Severity: timeline.SeverityError,
				Code:     timeline.CodeUnrecoverable,
				Message: fmt.Sprintf("ssrc %d: %d packets missing from sequence %d before %d",
					out.StreamSSRC, gap, uint16(prev+1), uint16(a.Seq)),
			})
		}
		prev = a.Seq
	}
	return out
}

// extendSeq maps a 16-bit sequence number to 64 bits using the running highest
// extended sequence seen so far. The 32768 tolerance places the number in the
// epoch nearest the live stream, so a forward wrap continues upward and a late
// pre-wrap packet maps back to the previous epoch.
func extendSeq(seq uint16, highest int64) int64 {
	if highest < 0 {
		return int64(seq)
	}
	// Epoch of the highest extended value.
	epoch := (highest / 65536) * 65536
	ext := epoch + int64(seq)
	if ext < highest-32768 {
		ext += 65536
	} else if ext > highest+32768 {
		ext -= 65536
	}
	return ext
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// MediaClock describes how an RTP timestamp maps onto media time.
type MediaClock struct {
	Base timeline.Base
	// FirstTimestamp is the RTP timestamp of the stream's first packet; it is
	// the zero point, so a recovered stream starts at media time zero.
	FirstTimestamp uint32
	// Unwrapper continues 32-bit timestamp rollover across the stream.
	unwrapper *timeline.Unwrapper
}

// NewMediaClock starts a clock at the given first timestamp.
func NewMediaClock(base timeline.Base, first uint32) (*MediaClock, error) {
	u, err := timeline.NewUnwrapper(timeline.Base{Name: base.Name, TimeScale: base.TimeScale, WrapAt: 1 << 32})
	if err != nil {
		return nil, err
	}
	return &MediaClock{Base: base, FirstTimestamp: first, unwrapper: u}, nil
}

// Tick returns the continuous media tick for a packet timestamp relative to
// the clock's zero point. Timestamps must be supplied in recovered order.
func (c *MediaClock) Tick(ts uint32) (int64, error) {
	cont, _, err := c.unwrapper.Push(int64(ts))
	if err != nil {
		return 0, err
	}
	return cont - int64(c.FirstTimestamp), nil
}

// RTPFrame is one reassembled frame, ordered by media time.
type RTPFrame struct {
	Timestamp uint32
	Payload   []byte
	// Complete is false when an unrecoverable gap fell inside the frame's
	// sequence range; Missing is the number of packets absent.
	Complete bool
	Missing  int
	// SeqSpan is the number of sequence positions the frame's packets cover.
	SeqSpan int
	// FirstSeq is the extended sequence number of the frame's first packet.
	FirstSeq int64
}

// AssembleFrames groups recovered packets into frames. A frame ends at a packet
// with the marker bit set, or when the timestamp changes. Received packets per
// frame are counted exactly, so Missing reflects real loss rather than span
// arithmetic, and a frame spanning a sequence gap is marked incomplete rather
// than silently joined across the hole.
func AssembleFrames(recovery RTPRecovery) []RTPFrame {
	var frames []RTPFrame
	var cur *RTPFrame
	count := 0
	for _, pe := range recovery.Packets {
		if cur == nil || pe.Pkt.Timestamp != cur.Timestamp {
			if cur != nil {
				cur.Missing = cur.SeqSpan - count
				cur.Complete = cur.Missing == 0
				frames = append(frames, *cur)
			}
			cur = &RTPFrame{Timestamp: pe.Pkt.Timestamp, FirstSeq: pe.Seq}
			count = 0
		}
		cur.Payload = append(cur.Payload, pe.Pkt.Payload...)
		cur.SeqSpan = int(pe.Seq-cur.FirstSeq) + 1
		count++
		if pe.Pkt.Marker {
			cur.Missing = cur.SeqSpan - count
			cur.Complete = cur.Missing == 0
			frames = append(frames, *cur)
			cur = nil
			count = 0
		}
	}
	if cur != nil {
		cur.Missing = cur.SeqSpan - count
		cur.Complete = cur.Missing == 0
		frames = append(frames, *cur)
	}
	return frames
}
