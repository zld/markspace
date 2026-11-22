package remux

import (
	"github.com/pion/rtp"
)

// RTPStats reports reorder-window recovery over one RTP stream.
type RTPStats struct {
	Sent       int     `json:"sent"`
	Delivered  int     `json:"delivered"`
	Duplicates int     `json:"duplicates"`
	Lost       int     `json:"lost"`
	Rate       float64 `json:"rate"`
}

// seqLess reports whether a is "before" b in 16-bit RTP sequence space.
func seqLess(a, b uint16) bool {
	return int16(a-b) < 0
}

// ReorderRTP restores packet order from a stream containing reordering,
// duplicates and losses, using a fixed reorder window. It returns the
// deliverable packets in sequence order plus recovery statistics. The
// media clock is untouched: only transport sequencing is inspected.
func ReorderRTP(pkts []*rtp.Packet, window int) ([]*rtp.Packet, RTPStats) {
	if window < 1 {
		window = 1
	}
	var st RTPStats
	st.Sent = len(pkts)
	if len(pkts) == 0 {
		st.Rate = 1
		return nil, st
	}
	expected := pkts[0].SequenceNumber
	buf := map[uint16]*rtp.Packet{}
	var out []*rtp.Packet
	flush := func(force bool) {
		for {
			if p, ok := buf[expected]; ok {
				out = append(out, p)
				delete(buf, expected)
				expected++
				continue
			}
			if force && len(buf) > 0 {
				// Find the smallest buffered seq ahead of expected.
				var best uint16
				found := false
				for s := range buf {
					if !found || seqLess(s, best) {
						best, found = s, true
					}
				}
				st.Lost += int(uint16(best - expected))
				expected = best
				continue
			}
			if len(buf) > window {
				// Gap beyond the window: deem the missing packet lost.
				st.Lost++
				expected++
				continue
			}
			return
		}
	}
	for _, p := range pkts {
		seq := p.SequenceNumber
		if seqLess(seq, expected) {
			st.Duplicates++ // late duplicate of an already delivered seq
			continue
		}
		if _, dup := buf[seq]; dup {
			st.Duplicates++
			continue
		}
		buf[seq] = p
		flush(false)
	}
	// Drain: anything still buffered is delivered; remaining gaps lost.
	for len(buf) > 0 {
		flush(true)
	}
	st.Delivered = len(out)
	total := st.Delivered + st.Lost
	if total > 0 {
		st.Rate = float64(st.Delivered) / float64(total)
	} else {
		st.Rate = 1
	}
	return out, st
}

// SimulateLoss deterministically applies reordering, duplication and
// loss to an ordered packet list, modelling an unreliable network
// return path. Every 17th packet is delayed past its successor
// (reorder), every 23rd is duplicated, every 19th is dropped.
func SimulateLoss(in []*rtp.Packet) []*rtp.Packet {
	var out []*rtp.Packet
	var delayed *rtp.Packet
	for i, p := range in {
		n := i + 1
		if n%19 == 0 {
			continue // lost on the wire
		}
		if n%17 == 0 {
			delayed = p // delivered after the next packet
			continue
		}
		out = append(out, p)
		if delayed != nil {
			out = append(out, delayed)
			delayed = nil
		}
		if n%23 == 0 {
			out = append(out, p) // duplicated by the network
		}
	}
	if delayed != nil {
		out = append(out, delayed)
	}
	return out
}
