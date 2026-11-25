// Package segment turns scanned access units into timeline-aligned clips.
// Boundaries are left-closed/right-open on the shared nanosecond timeline,
// and every corrupt access unit makes its containing segment corrupt while
// the segment is still emitted.
package segment

import (
	"mediad/internal/clock"
	"mediad/internal/stream"
)

// Segment is one deliverable clip covering a contiguous run of access units.
type Segment struct {
	ID       uint64         `json:"id"`
	Interval clock.Interval `json:"interval"`
	AUStart  uint64         `json:"au_start"`
	AUEnd    uint64         `json:"au_end"` // exclusive
	ByteStart int64         `json:"byte_start"`
	ByteEnd   int64         `json:"byte_end"` // exclusive
	Corrupt  bool           `json:"corrupt"`
	Reasons  []string       `json:"reasons,omitempty"`
	Payload  []byte         `json:"-"`
}

// Cutter groups access units into segments using the shared time base.
type Cutter struct {
	tb *clock.TimeBase
}

// NewCutter binds a cutter to the pipeline time base.
func NewCutter(tb *clock.TimeBase) *Cutter { return &Cutter{tb: tb} }

// Cut assigns every access unit to exactly one segment. By default each AU
// becomes its own segment; AUsPerSegment > 1 merges consecutive AUs. Corrupt
// AUs propagate the flag and their reasons, so segments spanning a damaged
// region are emitted with a corruption marker rather than dropped.
func (c *Cutter) Cut(aus []*stream.AccessUnit, ausPerSegment int) []*Segment {
	if ausPerSegment <= 1 || len(aus) == 0 {
		out := make([]*Segment, 0, len(aus))
		for _, au := range aus {
			out = append(out, c.build(au.Index, []*stream.AccessUnit{au}))
		}
		return out
	}
	var out []*Segment
	for start := 0; start < len(aus); start += ausPerSegment {
		end := start + ausPerSegment
		if end > len(aus) {
			end = len(aus)
		}
		out = append(out, c.build(aus[start].Index, aus[start:end]))
	}
	return out
}

func (c *Cutter) build(id uint64, group []*stream.AccessUnit) *Segment {
	first, last := group[0], group[len(group)-1]
	seg := &Segment{
		ID:        id,
		Interval:  clock.Interval{StartNS: c.tb.FrameToNS(first.Index), EndNS: c.tb.FrameEndNS(last.Index)},
		AUStart:   first.Index,
		AUEnd:     last.Index + 1,
		ByteStart: first.Start,
		ByteEnd:   last.End,
	}
	for _, au := range group {
		if au.Corrupt {
			seg.Corrupt = true
			seg.Reasons = append(seg.Reasons, au.Reasons...)
		}
		seg.Payload = append(seg.Payload, auPayload(au)...)
	}
	return seg
}

// auPayload concatenates the retained (cap-bounded) NAL payloads of one AU.
func auPayload(au *stream.AccessUnit) []byte {
	var buf []byte
	for _, n := range au.NALs {
		buf = append(buf, n.Data...)
	}
	return buf
}
