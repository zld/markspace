package ingest

import (
	"fmt"
	"sort"
)

// Disposition describes how the sequence tracker treated an input packet.
type Disposition string

const (
	DispositionAccepted   Disposition = "accepted"
	DispositionDuplicate  Disposition = "duplicate"
	DispositionOutOfOrder Disposition = "out_of_order"
)

// Classification is the result of extending and checking a sequence number.
type Classification struct {
	Extended    uint64
	Disposition Disposition
	// FilledGap is true when an out-of-order arrival completes a known hole.
	FilledGap bool
}

// Gap describes a still-missing extended sequence interval.
type Gap struct {
	First uint64 `json:"first"`
	Last  uint64 `json:"last"`
	Count int    `json:"count"`
}

// Tracker implements RFC-style 16-bit sequence unwrapping with a high-water
// mark. It correctly handles wrap, moderate reordering and duplicates.
type Tracker struct {
	initialized bool
	maxExtended uint64
	seen        map[uint64]struct{}
	missing     map[uint64]struct{}
	Duplicates  int
	Reordered   int
}

// NewTracker creates an empty sequence tracker.
func NewTracker() *Tracker {
	return &Tracker{seen: make(map[uint64]struct{}), missing: make(map[uint64]struct{})}
}

// Extend maps a uint16 RTP sequence number into a monotonically usable uint64.
func (t *Tracker) Extend(seq uint16) uint64 {
	extended := t.resolve(seq)
	if !t.initialized {
		t.initialized = true
		t.maxExtended = extended
		t.seen[extended] = struct{}{}
		return extended
	}

	if _, dup := t.seen[extended]; dup {
		t.Duplicates++
		return extended
	}

	if extended > t.maxExtended {
		for missing := t.maxExtended + 1; missing < extended; missing++ {
			t.missing[missing] = struct{}{}
		}
		t.maxExtended = extended
	} else if _, wasMissing := t.missing[extended]; wasMissing {
		delete(t.missing, extended)
		t.Reordered++
	} else if extended < t.maxExtended {
		t.Reordered++
	}
	t.seen[extended] = struct{}{}
	return extended
}

// resolve performs RFC 1982-style unwrapping without mutating tracker state.
func (t *Tracker) resolve(seq uint16) uint64 {
	if !t.initialized {
		return uint64(seq)
	}
	max16 := uint16(t.maxExtended)
	delta := int32(seq) - int32(max16)
	// Signed half-window comparison around the current high-water mark.
	if delta > 32767 {
		delta -= 65536
	} else if delta < -32768 {
		delta += 65536
	}
	return uint64(int64(t.maxExtended) + int64(delta))
}

// Classify extends seq and reports whether it is new or a duplicate.
func (t *Tracker) Classify(seq uint16) Classification {
	extended := t.resolve(seq)
	_, filledGap := t.missing[extended]

	beforeDups := t.Duplicates
	beforeReorder := t.Reordered
	t.Extend(seq)
	c := Classification{Extended: extended, FilledGap: filledGap}
	switch {
	case t.Duplicates > beforeDups:
		c.Disposition = DispositionDuplicate
	case t.Reordered > beforeReorder:
		c.Disposition = DispositionOutOfOrder
	default:
		c.Disposition = DispositionAccepted
	}
	return c
}

// Missing returns the sorted extended sequence numbers still absent.
func (t *Tracker) Missing() []uint64 {
	out := make([]uint64, 0, len(t.missing))
	for seq := range t.missing {
		out = append(out, seq)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Gaps coalesces adjacent missing sequence numbers into ranges.
func (t *Tracker) Gaps() []Gap {
	missing := t.Missing()
	if len(missing) == 0 {
		return nil
	}
	gaps := []Gap{{First: missing[0], Last: missing[0], Count: 1}}
	for _, seq := range missing[1:] {
		last := &gaps[len(gaps)-1]
		if seq == last.Last+1 {
			last.Last = seq
			last.Count++
			continue
		}
		gaps = append(gaps, Gap{First: seq, Last: seq, Count: 1})
	}
	return gaps
}

// MaxExtended returns the greatest extended sequence observed.
func (t *Tracker) MaxExtended() (uint64, bool) {
	return t.maxExtended, t.initialized
}

// String renders a gap for logs and structured messages.
func (g Gap) String() string {
	if g.First == g.Last {
		return fmt.Sprintf("seq=%d", g.First)
	}
	return fmt.Sprintf("seq=%d..%d count=%d", g.First, g.Last, g.Count)
}
