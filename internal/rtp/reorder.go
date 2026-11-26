package rtp

import (
	"fmt"
	"sort"

	"rtx/internal/model"
)

// ExtendedSeq is a sequence number unwrapped into a monotonically increasing
// 64-bit domain. wrap counts how many times the 16-bit space rolled over.
type ExtendedSeq struct {
	Seq  uint16 `json:"seq"`
	Wrap int64  `json:"wrap"`
}

// Value is the unwrapped sequence value.
func (e ExtendedSeq) Value() int64 { return e.Wrap<<16 + int64(e.Seq) }

// String renders seq@wrap for diagnostics.
func (e ExtendedSeq) String() string { return fmt.Sprintf("%d@w%d", e.Seq, e.Wrap) }

// Stream is a single RTP source after sequence restoration. Packets are
// ordered by unwrapped sequence, with duplicates removed by first occurrence.
type Stream struct {
	Source  string
	Packets []Packet
	Ext     []ExtendedSeq
	// Gaps lists unwrapped sequences that are missing from this stream.
	Gaps []int64
	// Duplicates counts packets dropped because their unwrapped sequence had
	// already been seen.
	Duplicates int
	// OutOfOrder counts packets whose recorded position was earlier than a
	// previous packet's unwrapped sequence.
	OutOfOrder int
	// Wraps counts 16-bit sequence wrap-arounds observed.
	Wraps int
}

// SplitSources groups packets by source, preserving first-seen source order.
func (t *Trace) SplitSources() []*Stream {
	order := t.Sources()
	bySource := make(map[string][]Packet, len(order))
	for _, p := range t.Packets {
		bySource[p.Source] = append(bySource[p.Source], p)
	}
	out := make([]*Stream, 0, len(order))
	for _, src := range order {
		out = append(out, &Stream{Source: src, Packets: bySource[src]})
	}
	return out
}

// Reorder restores sequence order for every source in the trace.
func Reorder(t *Trace) ([]*Stream, error) {
	streams := t.SplitSources()
	for _, s := range streams {
		if err := s.Restore(); err != nil {
			return nil, err
		}
	}
	return streams, nil
}

// Restore unwraps sequence numbers across 16-bit wrap, sorts by unwrapped
// sequence, drops exact duplicates, and records gaps. It never invents
// packets for a gap: a missing sequence is only reported.
func (s *Stream) Restore() error {
	if len(s.Packets) == 0 {
		return nil
	}
	// The first packet recorded defines the reference frame; unwrapping is
	// relative to it so that the first packet is always seq 0@wrap 0.
	ref := s.Packets[0].Seq
	type entry struct {
		pkt Packet
		ext ExtendedSeq
		pos int
	}
	entries := make([]entry, 0, len(s.Packets))
	base := int64(ref)
	last := base
	for i, p := range s.Packets {
		delta := int32(int16(p.Seq - ref)) // shortest signed distance
		// A jump of exactly half the sequence space is ambiguous: both the
		// forward and backward interpretation are equally likely.
		if delta == -32768 {
			return model.E(CodeRTPSequence, "reorder", "source %s: ambiguous 16-bit jump from %d to %d", s.Source, ref, p.Seq)
		}
		// Extend relative to the first recorded packet, so the extended
		// stream keeps the packet's own low 16 bits and stays consistent.
		v := base + int64(delta)
		if v < last-32768 || v > last+32768 {
			return model.E(CodeRTPSequence, "reorder", "source %s: sequence jump %d -> %d exceeds half the sequence space", s.Source, last, v)
		}
		last = v
		es := ExtendedSeq{Seq: p.Seq, Wrap: floorDiv(v, 1<<16)}
		entries = append(entries, entry{pkt: p, ext: es, pos: i})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].ext.Value() < entries[j].ext.Value()
	})

	s.Packets = s.Packets[:0]
	s.Ext = s.Ext[:0]
	seen := make(map[int64]bool, len(entries))
	var prev int64
	first := true
	prevRecorded := -1
	for _, e := range entries {
		v := e.ext.Value()
		if seen[v] {
			s.Duplicates++
			continue
		}
		seen[v] = true
		if !first {
			if floorDiv(v, 1<<16) != floorDiv(prev, 1<<16) {
				s.Wraps++
			}
			if v > prev+1 {
				// Report every missing sequence in the gap, but cap the list
				// so a pathological capture cannot exhaust memory.
				for gap := prev + 1; gap < v && len(s.Gaps) < 1<<16; gap++ {
					s.Gaps = append(s.Gaps, gap)
				}
			}
		}
		if e.pos < prevRecorded {
			s.OutOfOrder++
		}
		prevRecorded = e.pos
		prev = v
		first = false
		s.Packets = append(s.Packets, e.pkt)
		s.Ext = append(s.Ext, e.ext)
	}
	return nil
}

// WrapCount reports how many 16-bit rollovers the restored stream crossed.
func (s *Stream) WrapCount() int { return s.Wraps }

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
