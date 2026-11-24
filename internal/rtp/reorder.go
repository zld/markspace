// Package rtp recovers media packets from a lossy, out-of-order RTP feed and
// schedules representations under changing bandwidth. Sequence numbers are
// expanded across 16 bit wraparound, duplicates are counted once, and late
// packets never rewrite an already confirmed boundary.
package rtp

import (
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"archive/internal/clock"
)

// Bounds keep hostile input from allocating without limit.
const (
	MaxPackets    = 1_000_000
	MaxPayload    = 8192
	SeqModulus    = 1 << 16
	DefaultWindow = 64
)

// Packet is one decoded RTP packet in arrival order.
type Packet struct {
	Seq       uint16
	Timestamp uint32
	Marker    bool
	Payload   []byte
	Arrival   int
	WireSize  int
}

// Ordered is a delivered packet with expanded sequence and timestamp.
type Ordered struct {
	Packet
	SeqExt int64
	TSExt  int64
}

// Gap is a missing sequence range, retained as a recoverable marker.
type Gap struct {
	Start       uint16
	End         uint16
	Count       int
	Recoverable bool
	// Reason is "missing" for never-received packets and "late_dropped" when
	// a packet arrived after its boundary was confirmed.
	Reason string
}

// Result is the recovered transport state for one layer.
type Result struct {
	Ordered    []Ordered
	Duplicates int
	Reordered  int
	Late       int
	Lost       int
	Gaps       []Gap
	Notes      []string
}

// Decode unmarshals wire packets with pion/rtp, preserving arrival order.
func Decode(wire [][]byte) ([]Packet, error) {
	if len(wire) > MaxPackets {
		return nil, clock.Newf(clock.KindOutOfRange, "packet count %d exceeds limit %d", len(wire), MaxPackets)
	}
	out := make([]Packet, 0, len(wire))
	for i, b := range wire {
		var p rtp.Packet
		if err := p.Unmarshal(b); err != nil {
			return nil, clock.Newf(clock.KindInvalidMetadata, "rtp packet %d is malformed: %v", i, err).
				WithLocation(fmt.Sprintf("arrival %d", i)).WithCause(err)
		}
		if len(p.Payload) > MaxPayload {
			return nil, clock.Newf(clock.KindOutOfRange,
				"rtp packet %d payload %d exceeds limit %d", i, len(p.Payload), MaxPayload).
				WithLocation(fmt.Sprintf("arrival %d seq %d", i, p.SequenceNumber))
		}
		out = append(out, Packet{
			Seq:       p.SequenceNumber,
			Timestamp: p.Timestamp,
			Marker:    p.Marker,
			Payload:   append([]byte(nil), p.Payload...),
			Arrival:   i,
			WireSize:  len(b),
		})
	}
	return out, nil
}

// Reorder deduplicates and reorders packets. The watermark of confirmed
// sequence numbers advances only over contiguous data or after a packet falls
// outside the reorder window; later arrivals below the watermark are recorded
// as late and dropped without changing the established boundary.
func Reorder(in []Packet) (*Result, error) {
	if len(in) > MaxPackets {
		return nil, clock.Newf(clock.KindOutOfRange, "packet count %d exceeds limit %d", len(in), MaxPackets)
	}
	res := &Result{}
	if len(in) == 0 {
		return res, nil
	}
	buffer := make(map[int64]Packet, len(in))
	seen := make(map[int64]bool, len(in))
	pendingGaps := make(map[int64]Gap)
	var maxExt int64
	next := int64(in[0].Seq)

	recordGap := func(from, to int64, reason string) {
		if to < from {
			return
		}
		for e := from; e <= to; e++ {
			if seen[e] {
				continue
			}
			pendingGaps[e] = Gap{Start: uint16(e), End: uint16(e), Count: 1, Recoverable: true, Reason: reason}
		}
	}
	flush := func() {
		for {
			p, ok := buffer[next]
			if !ok {
				return
			}
			delete(buffer, next)
			res.Ordered = append(res.Ordered, Ordered{Packet: p, SeqExt: next})
			next++
		}
	}

	for i := range in {
		p := in[i]
		if len(p.Payload) > MaxPayload {
			return nil, clock.Newf(clock.KindOutOfRange,
				"payload %d exceeds limit %d", len(p.Payload), MaxPayload).
				WithLocation(fmt.Sprintf("seq %d", p.Seq))
		}
		var ext int64
		if i == 0 {
			ext = int64(p.Seq)
		} else {
			var err error
			ext, err = clock.Unwrap(uint32(p.Seq), maxExt, SeqModulus)
			if err != nil {
				return nil, err
			}
			// A wildly older packet is treated as late by watermark rather
			// than allowed to drag expansion backwards.
			if ext < next-SeqModulus/2 {
				ext += SeqModulus
			}
		}
		if ext < maxExt {
			res.Reordered++
		}
		if ext > maxExt {
			maxExt = ext
		}
		if seen[ext] {
			res.Duplicates++
			res.Notes = append(res.Notes, fmt.Sprintf("duplicate seq %d dropped (arrival %d)", uint16(ext), p.Arrival))
			continue
		}
		seen[ext] = true
		if ext < next {
			res.Late++
			res.Notes = append(res.Notes, fmt.Sprintf("late seq %d ignored; boundary confirmed at %d", uint16(ext), uint16(next)))
			pendingGaps[ext] = Gap{Start: uint16(ext), End: uint16(ext), Count: 1, Recoverable: true, Reason: "late_dropped"}
			continue
		}
		buffer[ext] = p
		flush()
		if maxExt-next > DefaultWindow {
			cutoff := maxExt - DefaultWindow
			recordGap(next, cutoff-1, "missing")
			next = cutoff
			flush()
		}
	}

	// Finalize: deliver remaining buffered packets in sequence order, marking
	// every hole up to the highest observed sequence as a recoverable gap.
	keys := make([]int64, 0, len(buffer))
	for k := range buffer {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	for _, k := range keys {
		if k > next {
			recordGap(next, k-1, "missing")
		}
		p := buffer[k]
		res.Ordered = append(res.Ordered, Ordered{Packet: p, SeqExt: k})
		next = k + 1
	}
	if maxExt >= next {
		recordGap(next, maxExt, "missing")
	}

	gapKeys := make([]int64, 0, len(pendingGaps))
	for k := range pendingGaps {
		gapKeys = append(gapKeys, k)
	}
	sort.Slice(gapKeys, func(a, b int) bool { return gapKeys[a] < gapKeys[b] })
	for _, k := range gapKeys {
		g := pendingGaps[k]
		if n := len(res.Gaps); n > 0 && int64(res.Gaps[n-1].End)+1 == int64(g.Start) &&
			res.Gaps[n-1].Reason == g.Reason {
			res.Gaps[n-1].End = g.End
			res.Gaps[n-1].Count++
			continue
		}
		res.Gaps = append(res.Gaps, g)
	}
	for _, g := range res.Gaps {
		res.Lost += g.Count
	}

	// Expand timestamps in delivery order so wraparound is unwrapped once.
	var refTS int64
	for i := range res.Ordered {
		if i == 0 {
			refTS = int64(res.Ordered[i].Timestamp)
			res.Ordered[i].TSExt = refTS
			continue
		}
		ext, err := clock.Unwrap(res.Ordered[i].Timestamp, refTS, 1<<32)
		if err != nil {
			return nil, err
		}
		res.Ordered[i].TSExt = ext
		refTS = ext
	}
	return res, nil
}
