// Package buffer implements the bounded reorder (jitter) buffer that sits
// between RTP sequence recovery and the timeline. Packets are held until the
// gap before them is closed or the reorder window expires, then released in
// extended sequence order. Packets older than the late window are surfaced as
// late events instead of being silently dropped.
package buffer

import (
	"sort"

	"mediasync/internal/model"
)

// Config bounds the buffer. All limits are explicit so memory use is
// predictable for long captures.
type Config struct {
	// LateWindow is how far behind the newest packet an arrival may be while
	// still being treated as reorder rather than as a late event.
	LateWindow model.Nanos
	// ReorderWindow is the maximum sequence distance below the highest
	// observed packet that is still buffered. Zero means buffer only the
	// contiguous prefix.
	ReorderWindow int
	// MaxPackets caps buffered entries; exceeding it is a hard error rather
	// than unbounded growth.
	MaxPackets int
}

// Entry is one packet as the buffer sees it. MediaTime is the uncorrected
// media timestamp on the kernel time base; Arrival is the capture clock.
type Entry struct {
	Ext       int64
	TrackID   uint32
	Kind      model.TrackKind
	Keyframe  bool
	MediaTime model.Nanos
	Arrival   model.Nanos
	Size      int
}

// Status classifies the result of Push.
type Status int

const (
	// Buffered means the entry is held waiting for a gap to fill.
	Buffered Status = iota
	// Released means one or more entries became ready.
	Released
	// Late means the entry arrived after the late window and is reported as
	// a late event; it is not buffered.
	Late
)

// LateEvent records a packet that arrived outside the reorder window.
type LateEvent struct {
	Entry Entry
	// Behind is how far the packet's arrival trails the newest arrival seen
	// when it was offered.
	Behind model.Nanos
}

// JitterBuffer is a single-track reorder buffer. It is deterministic given
// the same sequence of Push calls.
type JitterBuffer struct {
	cfg      Config
	pending  map[int64]Entry
	highExt  int64
	highInit bool
	maxArr   model.Nanos
	arrInit  bool
	released int64
	relInit  bool
}

// New constructs a buffer, validating its bounds.
func New(cfg Config) (*JitterBuffer, error) {
	if cfg.MaxPackets <= 0 {
		return nil, model.NewError(model.CodeBufferOverflow, model.Loc{TrackIndex: -1}, "MaxPackets must be positive, got %d", cfg.MaxPackets)
	}
	if cfg.ReorderWindow < 0 {
		return nil, model.NewError(model.CodeBufferOverflow, model.Loc{TrackIndex: -1}, "ReorderWindow must not be negative, got %d", cfg.ReorderWindow)
	}
	if cfg.LateWindow < 0 {
		return nil, model.NewError(model.CodeBufferOverflow, model.Loc{TrackIndex: -1}, "LateWindow must not be negative, got %d", cfg.LateWindow)
	}
	return &JitterBuffer{cfg: cfg, pending: make(map[int64]Entry)}, nil
}

// WouldBeLate reports whether an entry would be rejected as late without
// mutating any buffer state. It lets the caller rewind deterministically
// before committing to the event.
func (b *JitterBuffer) WouldBeLate(e Entry) bool {
	if b.arrInit && e.Arrival < b.maxArr-b.cfg.LateWindow {
		return true
	}
	return false
}

// Push offers an entry. For Released it returns every entry that is now ready
// in ascending extended sequence order; for Late it returns the late event and
// leaves the buffer untouched.
func (b *JitterBuffer) Push(e Entry) (Status, []Entry, *LateEvent) {
	if b.arrInit && e.Arrival < b.maxArr-b.cfg.LateWindow {
		return Late, nil, &LateEvent{Entry: e, Behind: b.maxArr - e.Arrival}
	}
	if !b.arrInit || e.Arrival > b.maxArr {
		b.maxArr = e.Arrival
		b.arrInit = true
	}
	if !b.highInit || e.Ext > b.highExt {
		b.highExt = e.Ext
		b.highInit = true
	}
	if _, exists := b.pending[e.Ext]; exists {
		// A duplicate is handled by the sequence layer; do not double-buffer.
		return Buffered, nil, nil
	}
	if b.relInit && e.Ext <= b.released {
		// Already released: this is a late event, not a duplicate. The
		// sequence layer is responsible for duplicate suppression.
		return Late, nil, &LateEvent{Entry: e, Behind: b.maxArr - e.Arrival}
	}
	// Bound the buffer before growing it.
	if len(b.pending) >= b.cfg.MaxPackets {
		return Late, nil, &LateEvent{Entry: e, Behind: 0}
	}
	b.pending[e.Ext] = e
	return Buffered, b.drain(), nil
}

// drain releases entries that can no longer be reordered. Any entry more than
// ReorderWindow packets behind the highest observed sequence is forced out;
// after that the contiguous run beginning at the release boundary is emitted.
// An unfilled hole therefore waits for its lookahead budget before it is
// treated as loss.
func (b *JitterBuffer) drain() []Entry {
	if len(b.pending) == 0 {
		return nil
	}
	limit := b.highExt - int64(b.cfg.ReorderWindow)
	keys := make([]int64, 0, len(b.pending))
	for k := range b.pending {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	var out []Entry
	for _, k := range keys {
		if k > limit {
			break
		}
		out = append(out, b.pending[k])
		delete(b.pending, k)
	}
	next := int64(-1)
	if len(out) > 0 {
		next = out[len(out)-1].Ext + 1
	} else if b.relInit {
		next = b.released + 1
	} else {
		for k := range b.pending {
			if next == -1 || k < next {
				next = k
			}
		}
	}
	for next != -1 {
		e, ok := b.pending[next]
		if !ok {
			break
		}
		out = append(out, e)
		delete(b.pending, next)
		next++
	}
	return b.finish(out)
}

func (b *JitterBuffer) finish(out []Entry) []Entry {
	if len(out) == 0 {
		return nil
	}
	maxExt := out[0].Ext
	for _, e := range out[1:] {
		if e.Ext > maxExt {
			maxExt = e.Ext
		}
	}
	b.released = maxExt
	b.relInit = true
	sort.Slice(out, func(i, j int) bool { return out[i].Ext < out[j].Ext })
	return out
}

// Flush releases every buffered entry in sequence order, for end of stream.
func (b *JitterBuffer) Flush() []Entry {
	out := make([]Entry, 0, len(b.pending))
	for _, e := range b.pending {
		out = append(out, e)
	}
	b.pending = make(map[int64]Entry)
	sort.Slice(out, func(i, j int) bool { return out[i].Ext < out[j].Ext })
	if len(out) > 0 {
		b.released = out[len(out)-1].Ext
		b.relInit = true
	}
	return out
}

// Len returns the number of buffered entries.
func (b *JitterBuffer) Len() int { return len(b.pending) }

// Stats reports the released boundary for checkpointing.
func (b *JitterBuffer) Stats() (released int64, ok bool, high int64, maxArr model.Nanos) {
	return b.released, b.relInit, b.highExt, b.maxArr
}

// Snapshot serializes the buffer's ordering state.
type Snapshot struct {
	Released   int64
	RelInit    bool
	HighExt    int64
	HighInit   bool
	MaxArrival model.Nanos
	ArrInit    bool
	Pending    []Entry
}

// Snapshot captures state for checkpointing.
func (b *JitterBuffer) Snapshot() Snapshot {
	p := make([]Entry, 0, len(b.pending))
	for _, e := range b.pending {
		p = append(p, e)
	}
	sort.Slice(p, func(i, j int) bool { return p[i].Ext < p[j].Ext })
	return Snapshot{
		Released: b.released, RelInit: b.relInit,
		HighExt: b.highExt, HighInit: b.highInit,
		MaxArrival: b.maxArr, ArrInit: b.arrInit,
		Pending: p,
	}
}

// Restore replaces buffer state from a snapshot.
func (b *JitterBuffer) Restore(s Snapshot) {
	b.pending = make(map[int64]Entry, len(s.Pending))
	for _, e := range s.Pending {
		b.pending[e.Ext] = e
	}
	b.released, b.relInit = s.Released, s.RelInit
	b.highExt, b.highInit = s.HighExt, s.HighInit
	b.maxArr, b.arrInit = s.MaxArrival, s.ArrInit
}
