package rtptrace

import "sort"

// Stats counts weak-network outcomes over one trace.
type Stats struct {
	Lost       int
	Duplicated int
	Reordered  int
	Delivered  int
}

// Plan is the reproducible impairment configuration.
type Plan struct {
	Seed        uint64
	LossRate    float64 // probability a packet is dropped
	DuplicateAt []int   // input packet indices to duplicate
	ReorderAt   []int   // input packet indices swapped with their successor
}

// Trace is the impaired packet order plus its statistics.
type Trace struct {
	Packets []Packet
	Stats   Stats
}

// Apply runs the plan deterministically. The same (packets, plan) always yields
// the same output. Packet contents, including SourceOffset, are never edited.
func Apply(packets []Packet, plan Plan) Trace {
	rng := newSplitMix(plan.Seed)
	lost := make(map[int]bool)
	for i := range packets {
		if plan.LossRate > 0 && rng.float64() < plan.LossRate {
			lost[i] = true
		}
	}

	type step struct {
		pkt  Packet
		dup  bool
		orig int // original packet index
	}
	steps := make([]step, 0, len(packets)+len(plan.DuplicateAt))
	dupSet := make(map[int]bool)
	for _, i := range plan.DuplicateAt {
		dupSet[i] = true
	}

	for i, pkt := range packets {
		if lost[i] {
			continue
		}
		steps = append(steps, step{pkt: pkt, dup: false, orig: i})
		if dupSet[i] {
			steps = append(steps, step{pkt: pkt, dup: true, orig: i})
		}
	}

	// Reorder each surviving pair (i, i+1) by swapping their primary slots.
	// A pair whose packets were lost or shifted by a duplicate is skipped rather
	// than silently rearranged.
	swapOrig := make(map[int]bool)
	for _, r := range plan.ReorderAt {
		swapOrig[r] = true
	}
	slotByOrig := make(map[int]int)
	for k, st := range steps {
		if !st.dup {
			slotByOrig[st.orig] = k
		}
	}
	for r := range swapOrig {
		a, okA := slotByOrig[r]
		b, okB := slotByOrig[r+1]
		if !okA || !okB {
			continue
		}
		steps[a], steps[b] = steps[b], steps[a]
	}

	out := make([]Packet, 0, len(steps))
	seenSeq := make(map[uint16]int)
	var stats Stats
	stats.Lost = len(lost)
	for _, st := range steps {
		if _, ok := seenSeq[st.pkt.Sequence]; ok {
			stats.Duplicated++
		}
		seenSeq[st.pkt.Sequence] = len(out)
		out = append(out, st.pkt)
	}
	stats.Reordered = countReorders(out)
	stats.Delivered = len(out)
	return Trace{Packets: out, Stats: stats}
}

// countReorders counts adjacent deliveries where the sequence number
// decreases, the observable out-of-order signature (wrapping-aware).
func countReorders(out []Packet) int {
	n := 0
	for i := 1; i < len(out); i++ {
		prev, cur := out[i-1].Sequence, out[i].Sequence
		if cur != prev+1 && int16(cur-prev) < 0 {
			n++
		}
	}
	return n
}

// MapBackToTimeline returns delivered packets ordered by source sample offset.
// It is a pure re-sort: no packet field and no source offset is ever modified.
func MapBackToTimeline(trace Trace) []Packet {
	out := append([]Packet(nil), trace.Packets...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SourceOffset != out[j].SourceOffset {
			return out[i].SourceOffset < out[j].SourceOffset
		}
		return out[i].Sequence < out[j].Sequence
	})
	return out
}

// splitMix64 is a tiny deterministic PRNG so impairment needs no global state.
type splitMix64 struct{ state uint64 }

func newSplitMix(seed uint64) *splitMix64 { return &splitMix64{state: seed} }

func (s *splitMix64) uint64() uint64 {
	s.state += 0x9e3779b97f4a7c15
	z := s.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (s *splitMix64) float64() float64 {
	return float64(s.uint64()>>11) / float64(uint64(1)<<53)
}
