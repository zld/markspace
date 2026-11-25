package stream

import (
	"sort"

	"mediacheck/internal/fault"
)

// DamageRegion is one contiguous damaged span: adjacent faults are
// merged so a multi-packet fragment reads as a single region.
type DamageRegion struct {
	Class   fault.Class
	Codec   Codec
	Seq     fault.SeqRange
	Byte    fault.ByteRange
	Time    fault.TimeRange
	NALType uint32
	Faults  []fault.Fault
}

// LocateDamage groups recorded faults into ordered, non-overlapping
// regions. Coordinates come straight from the fault model, so the
// regions share time units with the samples and never invent data.
func LocateDamage(res ScanResult) []DamageRegion {
	if len(res.Faults) == 0 {
		return nil
	}
	// Stable order: seq first, then byte offset, preserving insertion
	// order for identical coordinates (sort.SliceStable).
	fs := make([]fault.Fault, len(res.Faults))
	copy(fs, res.Faults)
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Seq.First != fs[j].Seq.First {
			return fs[i].Seq.First < fs[j].Seq.First
		}
		return fs[i].Byte.Offset < fs[j].Byte.Offset
	})

	regions := make([]DamageRegion, 0)
	for _, f := range fs {
		if len(regions) > 0 && canMerge(regions[len(regions)-1], f) {
			regions[len(regions)-1] = merge(regions[len(regions)-1], f)
			continue
		}
		regions = append(regions, DamageRegion{
			Class:   f.Class,
			Codec:   Codec(f.Codec),
			Seq:     f.Seq,
			Byte:    f.Byte,
			Time:    f.Time,
			NALType: f.NALType,
			Faults:  []fault.Fault{f},
		})
	}
	return regions
}

// canMerge combines same-class faults whose packet spans touch or
// overlap. Different classes stay distinct even when adjacent, because
// the acceptance report counts categories separately.
func canMerge(r DamageRegion, f fault.Fault) bool {
	if r.Class != f.Class {
		return false
	}
	return f.Seq.First <= r.Seq.Last+1 && f.Seq.Last+1 >= r.Seq.First
}

func merge(r DamageRegion, f fault.Fault) DamageRegion {
	if f.Seq.First < r.Seq.First {
		r.Seq.First = f.Seq.First
	}
	if f.Seq.Last > r.Seq.Last {
		r.Seq.Last = f.Seq.Last
	}
	r.Byte = span(r.Byte, f.Byte)
	if f.Time.Start < r.Time.Start || r.Time.Start == 0 {
		r.Time.Start = f.Time.Start
	}
	if f.Time.End > r.Time.End {
		r.Time.End = f.Time.End
	}
	r.Faults = append(r.Faults, f)
	return r
}

func span(a, b fault.ByteRange) fault.ByteRange {
	start := a.Offset
	if b.Offset < start {
		start = b.Offset
	}
	endA, endB := a.Offset+a.Length, b.Offset+b.Length
	end := endA
	if endB > end {
		end = endB
	}
	return fault.ByteRange{Offset: start, Length: end - start}
}
