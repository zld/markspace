// Package clock repairs 32-bit timestamp wraparound and source switches so
// every track becomes monotonic, keeping the raw value, the correction and
// the source byte offset for audit.
package clock

import "mediarc/internal/model"

// Sample is one raw timestamp read from a source (e.g. an RTP header),
// tied to the byte offset of the unit it describes.
type Sample struct {
	SrcOffset int64
	Raw       uint32
}

// Repaired is a sample mapped onto a monotonic 64-bit timeline.
type Repaired struct {
	SrcOffset int64      // byte offset in the source stream
	Raw       uint32     // original 32-bit timestamp, unmodified
	Ticks     int64      // corrected monotonic ticks
	Delta     int64      // correction applied: Ticks - Raw
	Event     model.Code // "", CLOCK_WRAP or SOURCE_SWITCH
}

const (
	wrapMod  = int64(1) << 32
	wrapHalf = uint32(1) << 31
)

// Repair unwraps a sequence of raw 32-bit timestamps into monotonic ticks.
// A backward step larger than half the range is a wraparound (add 2^32);
// any other backward step is a source switch and is rebased to continue one
// tick after the previous sample.
func Repair(samples []Sample) []Repaired {
	out := make([]Repaired, 0, len(samples))
	var high, prevTicks int64
	var prevRaw uint32
	for i, s := range samples {
		r := Repaired{SrcOffset: s.SrcOffset, Raw: s.Raw}
		switch {
		case i == 0:
			high = 0
		case s.Raw >= prevRaw:
			// forward: keep current wrap accumulator
		case prevRaw-s.Raw > wrapHalf:
			high += wrapMod
			r.Event = model.ErrWrap
		default:
			high = prevTicks + 1 - int64(s.Raw) // rebase across source switch
			r.Event = model.ErrSourceSwitch
		}
		r.Ticks = high + int64(s.Raw)
		r.Delta = r.Ticks - int64(s.Raw)
		prevRaw, prevTicks = s.Raw, r.Ticks
		out = append(out, r)
	}
	return out
}
