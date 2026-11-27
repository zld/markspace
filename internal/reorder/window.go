// Package reorder restores transmission order inside a bounded window,
// counts duplicate packets exactly once and marks packet loss as
// explicit missing intervals on the sample timeline.
package reorder

import (
	"bytes"
	"sort"

	merr "medialib/internal/errors"
	"medialib/internal/ingest"
)

// Gap is an explicit missing interval on the output timeline.
type Gap struct {
	SeqStart    uint32 // first missing extended sequence number
	Count       uint32 // missing packet count
	StartSample int64  // gap position in RTP clock ticks
	Samples     int64  // missing ticks
}

// Result is the outcome of one reorder pass.
type Result struct {
	Ordered     []ingest.Packet // deduplicated, in sequence order
	Duplicates  int             // packets dropped as exact retransmissions
	LostPackets int             // packets never received
	Gaps        []Gap           // missing intervals, in timeline order
}

// DurationOf reports the clock ticks carried by the packet with the
// given extended sequence number; codec frame durations may vary.
type DurationOf func(seq uint32) int64

// Window bounds how far a packet may arrive out of order.
type Window struct{ Size int }

type keyed struct {
	p   ingest.Packet
	ext int64
}

// Process dedups and orders pkts. Duplicate sequence numbers with
// differing payloads are corrupt input, not loss. Reordering deeper
// than the window is an error, never a silent timeline edit.
func (w Window) Process(pkts []ingest.Packet, dur DurationOf) (Result, error) {
	var res Result
	if len(pkts) == 0 {
		return res, merr.Newf(merr.StageReorder, -1, "window", "empty packet burst")
	}
	ref := int64(pkts[0].Seq)
	seen := map[int64]ingest.Packet{}
	var uniq []keyed
	for _, p := range pkts {
		ext := extend(int64(p.Seq), ref)
		if prev, ok := seen[ext]; ok {
			if prev.Timestamp != p.Timestamp || !bytes.Equal(prev.Payload, p.Payload) {
				return res, merr.Newf(merr.StageReorder, int64(p.Arrival),
					"dedup", "seq %d retransmitted with different content", p.Seq)
			}
			res.Duplicates++ // counted once, never twice on the timeline
			continue
		}
		seen[ext] = p
		uniq = append(uniq, keyed{p, ext})
	}
	sort.Slice(uniq, func(i, j int) bool { return uniq[i].ext < uniq[j].ext })
	// A packet's reorder depth is how many later-sequence packets it
	// arrived after; exceeding the window is a hard error.
	for _, k := range uniq {
		depth := 0
		for _, q := range pkts {
			if q.Arrival < k.p.Arrival && extend(int64(q.Seq), ref) > k.ext {
				depth++
			}
		}
		if depth > w.Size {
			return res, merr.Newf(merr.StageReorder, int64(k.p.Arrival),
				"window", "reorder depth %d exceeds window %d", depth, w.Size)
		}
	}
	min := uniq[0].ext
	for i, k := range uniq {
		if i == 0 {
			continue
		}
		prev := uniq[i-1]
		if d := k.ext - prev.ext; d > 1 {
			start := int64(prev.p.Timestamp) + dur(uint32(prev.ext-min))
			gap := Gap{
				SeqStart:    uint32(prev.ext - min + 1),
				Count:       uint32(d - 1),
				StartSample: start,
				Samples:     int64(k.p.Timestamp) - start,
			}
			res.Gaps = append(res.Gaps, gap)
			res.LostPackets += int(d - 1)
		}
	}
	res.Ordered = make([]ingest.Packet, len(uniq))
	for i, k := range uniq {
		res.Ordered[i] = k.p
	}
	return res, nil
}

// extend maps a 16-bit sequence number near ref onto the extended axis.
func extend(seq, ref int64) int64 {
	for seq-ref > 32768 {
		seq -= 65536
	}
	for ref-seq > 32768 {
		seq += 65536
	}
	return seq
}
