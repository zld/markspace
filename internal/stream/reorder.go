package stream

import (
	"sort"

	"mediasync/internal/clock"
	"mediasync/internal/fault"
)

// Duplicate records a sequence number observed more than once. FirstSeen is
// retained for deterministic ordering in reports.
type Duplicate struct {
	Sequence  uint16
	Offset    int64
	FirstSeen int64
}

// Gap is one contiguous run of missing sequence numbers.
type Gap struct {
	From uint16
	To   uint16
}

// Len is the number of missing packets in the gap.
func (g Gap) Len() int {
	return int(uint16(g.To-g.From)) + 1
}

// SequenceReport is the deterministic, sequence-ordered view of a stream.
type SequenceReport struct {
	Ordered    []*Packet
	Missing    []Gap
	Duplicates []Duplicate
	BaseSeq    uint16
	// ExtendedTS maps sequence number to monotonic 64-bit RTP timestamps.
	ExtendedTS map[uint16]int64
}

// Analyze deduplicates and reorders parsed packets by RTP sequence number and
// enumerates every gap. RTP timestamps are unwrapped onto a monotonic axis;
// ambiguous half-cycle wraps are reported rather than guessed.
func Analyze(packets []Packet) (*SequenceReport, error) {
	if len(packets) == 0 {
		return &SequenceReport{ExtendedTS: map[uint16]int64{}}, nil
	}
	base := packets[0].Sequence
	bySeq := map[uint16]*Packet{}
	var dups []Duplicate
	var minExt, maxExt uint32
	seen := 0

	for i := range packets {
		p := &packets[i]
		ext := uint32(base) + uint32(p.Sequence-base)
		if existing, ok := bySeq[p.Sequence]; ok {
			dups = append(dups, Duplicate{
				Sequence: p.Sequence, Offset: p.Offset, FirstSeen: existing.Offset,
			})
			p.Duplicate = true
			continue
		}
		bySeq[p.Sequence] = p
		if seen == 0 || ext < minExt {
			minExt = ext
		}
		if ext > maxExt {
			maxExt = ext
		}
		seen++
	}

	// Duplicates do not extend the observed span. Trailing loss is detected
	// from the gaps inside the span of distinct received sequences; a loss
	// past the last received packet is unknowable without FEC metadata.
	if maxExt-minExt >= 1<<16-1 {
		return nil, fault.New(fault.ErrPacketMalformed, "stream", -1,
			"sequence span covers a full 16-bit cycle; gaps are ambiguous")
	}

	sort.SliceStable(dups, func(i, j int) bool {
		if dups[i].Sequence != dups[j].Sequence {
			return dups[i].Sequence < dups[j].Sequence
		}
		return dups[i].Offset < dups[j].Offset
	})

	ordered := make([]*Packet, 0, maxExt-minExt+1)
	var gaps []Gap
	for ext := minExt; ext <= maxExt; ext++ {
		seq := uint16(ext)
		if p, ok := bySeq[seq]; ok {
			ordered = append(ordered, p)
			continue
		}
		if len(gaps) == 0 || gaps[len(gaps)-1].To+1 != seq {
			gaps = append(gaps, Gap{From: seq, To: seq})
		} else {
			gaps[len(gaps)-1].To = seq
		}
		ordered = append(ordered, &Packet{Sequence: seq, Recovered: false, Offset: -1})
	}

	// Unwrap RTP timestamps in sequence order over the received packets.
	// Ordered is already extended-sequence order, so filtering keeps order.
	received := make([]*Packet, 0, len(bySeq))
	for _, p := range ordered {
		if p.Offset >= 0 {
			received = append(received, p)
		}
	}
	rawTS := make([]uint32, len(received))
	offsets := make([]int64, len(received))
	for i, p := range received {
		rawTS[i] = p.RawTS
		offsets[i] = p.Offset
	}
	extTS, err := clock.ExtendRTPTimestamps("stream", rawTS, offsets)
	if err != nil {
		return nil, err
	}
	extended := make(map[uint16]int64, len(received))
	for i, p := range received {
		extended[p.Sequence] = extTS[i]
	}

	return &SequenceReport{
		Ordered:    ordered,
		Missing:    gaps,
		Duplicates: dups,
		BaseSeq:    base,
		ExtendedTS: extended,
	}, nil
}

// MissingCount sums the packet counts across all gaps.
func (r *SequenceReport) MissingCount() int {
	n := 0
	for _, g := range r.Missing {
		n += g.Len()
	}
	return n
}
