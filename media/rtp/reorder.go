// Package rtp ingests length-prefixed RTP audio packets, extends 16-bit
// sequence numbers across wraparound, reorders them inside a bounded media
// window, and reports losses, duplicates and late arrivals. It never
// fabricates audio for a missing packet.
package rtp

import (
	"encoding/binary"
	"fmt"
	"sort"

	pionrtp "github.com/pion/rtp"

	"replaykit/media/fault"
)

// DefaultReorderWindowMS is the media-time window used to hold out-of-order
// packets before giving up on them.
const DefaultReorderWindowMS = 80

// Packet is one parsed RTP audio packet plus its file provenance.
type Packet struct {
	Index      int    `json:"index"`       // position in the input stream
	Offset     int64  `json:"offset"`      // byte offset of the length prefix
	Seq        uint16 `json:"seq"`         // raw 16-bit sequence number
	Extended   int64  `json:"extended"`    // wrap-extended sequence number
	Timestamp  uint32 `json:"timestamp"`   // raw RTP timestamp
	SSRC       uint32 `json:"ssrc"`        //
	Marker     bool   `json:"marker"`      //
	PayloadLen int    `json:"payload_len"` //
	Arrival    int    `json:"arrival"`     // input order, 0-based
	Payload    []byte `json:"-"`           // codec payload
}

// Check is the outcome of ordering one stream.
type Check struct {
	Packets    []Packet        `json:"packets"`    // unique packets in media order
	Lost       []Packet        `json:"lost"`       // sequence gaps with no data
	Duplicates []Packet        `json:"duplicates"` // repeated sequence numbers
	Late       []Packet        `json:"late"`       // arrived after the window
	Anomalies  []fault.Anomaly `json:"anomalies"`  //
	Streams    int             `json:"streams"`    // distinct SSRC count
}

// ParseFramed reads the 2-byte big-endian length + raw packet framing.
// A malformed frame is skipped and reported by offset; a frame that declares
// a length past the buffer is never partially read.
func ParseFramed(data []byte, maxPacket int) ([]Packet, []fault.Anomaly, error) {
	if maxPacket <= 0 {
		return nil, nil, fmt.Errorf("rtp: max packet size must be positive")
	}
	var out []Packet
	var anomalies []fault.Anomaly
	off := int64(0)
	arrival := 0
	for off < int64(len(data)) {
		if int64(len(data))-off < 2 {
			anomalies = append(anomalies, fault.New("", off, fault.CategoryTruncatedBox, fault.SeverityError,
				fault.ActionSkipped, 0, fault.Range{}, "trailing byte cannot form a length prefix"))
			break
		}
		n := int(binary.BigEndian.Uint16(data[off:]))
		if n > maxPacket {
			anomalies = append(anomalies, fault.New("", off, fault.CategoryPacketMalformed, fault.SeverityError,
				fault.ActionSkipped, 0, fault.Range{}, fmt.Sprintf("declared packet length %d exceeds limit %d", n, maxPacket)))
			break
		}
		if int64(len(data))-off-2 < int64(n) {
			anomalies = append(anomalies, fault.New("", off, fault.CategoryTruncatedBox, fault.SeverityError,
				fault.ActionSkipped, 0, fault.Range{}, fmt.Sprintf("packet needs %d bytes but %d remain", n, int64(len(data))-off-2)))
			break
		}
		raw := data[off+2 : off+2+int64(n)]
		var pkt pionrtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			anomalies = append(anomalies, fault.New("", off, fault.CategoryPacketMalformed, fault.SeverityError,
				fault.ActionSkipped, 0, fault.Range{}, "rtp: "+err.Error()))
			off += 2 + int64(n)
			arrival++
			continue
		}
		out = append(out, Packet{
			Index: arrival, Offset: off, Arrival: arrival,
			Seq: pkt.SequenceNumber, Timestamp: pkt.Timestamp, SSRC: pkt.SSRC,
			Marker: pkt.Marker, PayloadLen: len(pkt.Payload),
			Payload: pkt.Payload,
		})
		off += 2 + int64(n)
		arrival++
	}
	return out, anomalies, nil
}

// ExtendSequence maps a 16-bit sequence number onto a monotonic int64,
// assuming forward progress and at most one wrap between neighbours.
func ExtendSequence(seq uint16, reference int64) int64 {
	base := reference &^ 0xFFFF
	best := base + int64(seq)
	bestDist := abs64(best - reference)
	for _, delta := range []int64{-0x10000, 0x10000} {
		if c := base + delta + int64(seq); abs64(c-reference) < bestDist {
			best, bestDist = c, abs64(c-reference)
		}
	}
	return best
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// OrderOptions configures Order.
type OrderOptions struct {
	// Window is the reorder window in media ticks (the audio timescale). A
	// packet whose timestamp is more than this behind the newest timestamp
	// already seen on arrival is reported late and excluded.
	Window int64
}

// WindowFromMS converts a millisecond window to ticks at the given timescale.
func WindowFromMS(ms, timescale int64) int64 {
	if timescale <= 0 || ms <= 0 {
		return 0
	}
	return ms * timescale / 1000
}

// DefaultOptions returns the standard 80 ms reorder window expressed in the
// audio timescale (sample rate). Callers that do not need a custom window use
// this so the documented default is applied consistently.
func DefaultOptions(timescale int) OrderOptions {
	return OrderOptions{Window: WindowFromMS(DefaultReorderWindowMS, int64(timescale))}
}

// Order extends, deduplicates and reorders one SSRC's packets. Duplicates
// are dropped so they can never be cut twice; gaps become Lost entries and
// are never filled. Output order is by RTP timestamp, then extended sequence.
func Order(packets []Packet, opts OrderOptions) Check {
	var res Check
	if len(packets) == 0 {
		return res
	}
	if opts.Window <= 0 {
		opts.Window = 1 << 20 // no practical limit unless configured
	}

	// Extend sequence numbers in arrival order, tracking the furthest
	// reference so wraparound is followed even with reordered arrivals.
	ref := int64(packets[0].Seq)
	seen := make(map[int64]Packet, len(packets))
	ssrcs := make(map[uint32]struct{})
	for _, p := range packets {
		p.Extended = ExtendSequence(p.Seq, ref)
		if p.Extended > ref {
			ref = p.Extended
		}
		ssrcs[p.SSRC] = struct{}{}
		if prev, dup := seen[p.Extended]; dup {
			res.Duplicates = append(res.Duplicates, p)
			res.Anomalies = append(res.Anomalies, fault.Anomaly{
				Layer: "audio", Offset: p.Offset, Category: fault.CategoryPacketDuplicate,
				Severity: fault.SeverityWarning, Action: fault.ActionDropped,
				MediaStart: int64(prev.Timestamp), MediaEnd: int64(p.Timestamp),
				Detail: fmt.Sprintf("duplicate seq=%d (extended %d) dropped instead of cut twice", p.Seq, p.Extended),
			})
			continue
		}
		seen[p.Extended] = p
	}
	res.Streams = len(ssrcs)

	// Decide lateness in arrival order: a packet that shows up after newer
	// media has already passed the window is late and cannot be placed.
	type arrival struct {
		p  Packet
		ts int64
	}
	ordered := make([]arrival, 0, len(seen))
	for _, p := range seen {
		ordered = append(ordered, arrival{p: p, ts: int64(p.Timestamp)})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].p.Arrival < ordered[j].p.Arrival })

	newest := make(map[uint32]int64)
	accepted := make([]Packet, 0, len(ordered))
	for _, a := range ordered {
		prev, ok := newest[a.p.SSRC]
		if ok && a.ts < prev-opts.Window {
			res.Late = append(res.Late, a.p)
			res.Anomalies = append(res.Anomalies, fault.Anomaly{
				Layer: "audio", Offset: a.p.Offset, Category: fault.CategoryPacketLate,
				Severity: fault.SeverityWarning, Action: fault.ActionSkipped,
				MediaStart: a.ts, MediaEnd: prev,
				Detail: fmt.Sprintf("seq=%d timestamp %d arrived %d ticks behind %d", a.p.Seq, a.ts, prev-a.ts, prev),
			})
			continue
		}
		if !ok || a.ts > prev {
			newest[a.p.SSRC] = a.ts
		}
		accepted = append(accepted, a.p)
	}

	sort.Slice(accepted, func(i, j int) bool {
		if accepted[i].Timestamp != accepted[j].Timestamp {
			return accepted[i].Timestamp < accepted[j].Timestamp
		}
		return accepted[i].Extended < accepted[j].Extended
	})
	res.Packets = accepted

	// Detect gaps on the extended sequence axis, per SSRC.
	bySSRC := make(map[uint32][]Packet)
	for _, p := range accepted {
		bySSRC[p.SSRC] = append(bySSRC[p.SSRC], p)
	}
	ssrcOrder := make([]uint32, 0, len(bySSRC))
	for ssrc := range bySSRC {
		ssrcOrder = append(ssrcOrder, ssrc)
	}
	sort.Slice(ssrcOrder, func(i, j int) bool { return ssrcOrder[i] < ssrcOrder[j] })
	for _, ssrc := range ssrcOrder {
		g := bySSRC[ssrc]
		sort.Slice(g, func(i, j int) bool { return g[i].Extended < g[j].Extended })
		for i := 1; i < len(g); i++ {
			prev, cur := g[i-1], g[i]
			gap := cur.Extended - prev.Extended - 1
			if gap <= 0 {
				continue
			}
			for k := int64(1); k <= gap; k++ {
				res.Lost = append(res.Lost, Packet{
					Seq: prev.Seq + uint16(k), Extended: prev.Extended + k, SSRC: ssrc,
					Timestamp: prev.Timestamp, PayloadLen: 0,
				})
			}
			res.Anomalies = append(res.Anomalies, fault.Anomaly{
				Layer: "audio", Offset: cur.Offset, Category: fault.CategoryPacketLost,
				Severity: fault.SeverityError, Action: fault.ActionNone,
				MediaStart: int64(prev.Timestamp), MediaEnd: int64(cur.Timestamp),
				Detail: fmt.Sprintf("%d packet(s) missing between extended seq %d and %d (no audio fabricated)", gap, prev.Extended, cur.Extended),
			})
		}
	}
	sort.SliceStable(res.Anomalies, func(i, j int) bool {
		if res.Anomalies[i].Offset != res.Anomalies[j].Offset {
			return res.Anomalies[i].Offset < res.Anomalies[j].Offset
		}
		return res.Anomalies[i].Category < res.Anomalies[j].Category
	})
	return res
}
