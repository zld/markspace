package rtpin

import (
	"sort"

	"mediakernel/internal/fault"
)

// Segment is a committed, in-order chunk of the reassembled file.
type Segment struct {
	Offset uint64
	Data   []byte
}

// Window bounds for the adaptive reorder wait window, in nanoseconds.
const (
	MinWindowNs = int64(1_000_000)     // 1 ms
	MaxWindowNs = int64(2_000_000_000) // 2 s
	baseWindow  = int64(50_000_000)    // 50 ms at reference bandwidth
	refBps      = 10_000_000.0         // 10 MB/s reference
)

// Reassembler deduplicates and reorders segmented packets by file offset.
// Commits are monotonic: once a byte range is committed it is never
// reordered again, so adapting the wait window never disturbs committed
// data.
type Reassembler struct {
	next    uint64            // next expected (uncommitted) offset
	pending map[uint64][]byte // buffered out-of-order payloads
	seen    map[uint64]bool   // every offset ever accepted (dedup)
	Dups    int               // duplicate packets dropped

	estBps   float64 // EMA bandwidth estimate, bytes per second
	lastArr  int64   // arrival time of previous packet, ns
	hasLast  bool
	windowNs int64 // current adaptive wait window
}

// NewReassembler starts reassembly at file offset 0.
func NewReassembler() *Reassembler {
	return &Reassembler{
		pending:  make(map[uint64][]byte),
		seen:     make(map[uint64]bool),
		windowNs: baseWindow,
	}
}

// WaitWindowNs returns the current adaptive wait window. It widens when the
// estimated bandwidth drops and narrows when the link is fast.
func (r *Reassembler) WaitWindowNs() int64 { return r.windowNs }

// BandwidthBps returns the current EMA bandwidth estimate in bytes/second.
func (r *Reassembler) BandwidthBps() float64 { return r.estBps }

// Add ingests one record. Duplicate payloads (same file offset already seen)
// are dropped and counted. Newly contiguous data is returned as committed
// segments in file order.
func (r *Reassembler) Add(rec Record) ([]Segment, error) {
	if len(rec.Payload) == 0 {
		return nil, fault.New(fault.ErrTruncatedPacket, rec.TraceOffset,
			"empty payload at file offset %d", rec.FileOffset)
	}
	r.updateEstimate(rec)
	if r.seen[rec.FileOffset] {
		r.Dups++
		return nil, nil
	}
	r.seen[rec.FileOffset] = true
	r.pending[rec.FileOffset] = rec.Payload
	return r.commit(), nil
}

// updateEstimate folds the arrival interval into the EMA bandwidth estimate
// and re-derives the wait window. Only future decisions observe the new
// window; committed ranges are immutable.
func (r *Reassembler) updateEstimate(rec Record) {
	if r.hasLast && rec.ArrivalNs > r.lastArr {
		dt := float64(rec.ArrivalNs-r.lastArr) / 1e9
		if dt > 0 {
			instant := float64(len(rec.Payload)) / dt
			const alpha = 0.25
			if r.estBps == 0 {
				r.estBps = instant
			} else {
				r.estBps = alpha*instant + (1-alpha)*r.estBps
			}
			w := int64(float64(baseWindow) * refBps / maxf(r.estBps, 1))
			r.windowNs = min64(max64(w, MinWindowNs), MaxWindowNs)
		}
	}
	if !r.hasLast || rec.ArrivalNs > r.lastArr {
		r.lastArr = rec.ArrivalNs
		r.hasLast = true
	}
}

// commit drains every pending payload contiguous with next, in order.
func (r *Reassembler) commit() []Segment {
	var out []Segment
	for {
		data, ok := r.pending[r.next]
		if !ok {
			return out
		}
		delete(r.pending, r.next)
		out = append(out, Segment{Offset: r.next, Data: data})
		r.next += uint64(len(data))
	}
}

// FirstMissing reports the first gap between the committed prefix and the
// lowest buffered (or expected total) offset. ok is false when no gap is
// known. Missing data is never fabricated; callers surface it as
// ERR_MISSING_SEGMENT.
func (r *Reassembler) FirstMissing() (start, end uint64, ok bool) {
	if len(r.pending) == 0 {
		return 0, 0, false
	}
	keys := make([]uint64, 0, len(r.pending))
	for k := range r.pending {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	if keys[0] > r.next {
		return r.next, keys[0], true
	}
	return 0, 0, false
}

// MissingError builds the canonical missing-segment error for the first gap.
func (r *Reassembler) MissingError() error {
	start, end, ok := r.FirstMissing()
	if !ok {
		return nil
	}
	return fault.New(fault.ErrMissingSegment, int64(start),
		"first missing byte range [%d,%d)", start, end)
}

// Pending returns the number of buffered out-of-order payloads.
func (r *Reassembler) Pending() int { return len(r.pending) }

// Committed returns the next uncommitted offset, i.e. the committed prefix
// length.
func (r *Reassembler) Committed() uint64 { return r.next }

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
