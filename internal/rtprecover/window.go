// Package rtprecover turns a lossy, reordered, duplicated RTP stream into an
// ordered sequence of samples while keeping memory bounded by the reorder
// window. It never fabricates missing media: gaps are reported explicitly and
// output resumes only when real packets arrive.
package rtprecover

import (
	"github.com/medialab/medialab/internal/clock"
	"github.com/pion/rtp"
)

// Payload flags carried inside the (self-generated) RTP payload envelope.
const (
	FlagKeyframe byte = 1 << iota
	FlagVideo
	FlagAudio
)

// DropKind explains why Push refused a packet.
type DropKind string

const (
	// DropDuplicate means the sequence number was already buffered, played
	// out or explicitly skipped as a confirmed hole.
	DropDuplicate DropKind = "duplicate"
	// DropLate means the packet cannot be ordered in: it sits behind the
	// playout head or jumps farther than the window can hold.
	DropLate DropKind = "late"
)

// Sample is one recovered, ordered media sample with its gap context.
type Sample struct {
	Seq       uint16
	TrackID   string
	AtTick    int64
	Keyframe  bool
	WaitingKF bool // true until the next keyframe after a gap is observed
	Flags     byte
	Body      []byte
	// GapStart/GapEnd describe the missing sequence span immediately before
	// this sample; both are zero when there was no gap.
	GapStart uint16
	GapEnd   uint16
	GapTicks clock.Range
}

// Stats counters exposed to the reporting stage.
type Stats struct {
	Received    uint64
	Duplicates  uint64
	LateDropped uint64
	ChecksumBad uint64
	Emitted     uint64
	PeakHeld    int
}

// seqDistance returns (b-a) interpreted as a signed uint16 delta.
func seqDistance(a, b uint16) int32 {
	return int32(int16(b - a))
}

// Window is a bounded ring buffer keyed by RTP sequence number. History of
// recently played/skipped sequence numbers is kept in a bitmap sized to twice
// the ring so duplicates arriving shortly after playout are still recognized.
type Window struct {
	capacity int
	slots    []*rtp.Packet
	filled   []bool
	base     uint16 // sequence number of slot 0, i.e. the playout head
	started  bool

	hist    []bool // relative positions of observed/skipped packets
	histOff int32  // position of hist[0] relative to base
	stats   Stats
}

// NewWindow creates a reorder window holding at most capacity in-flight
// packets; non-positive capacity is normalized to one slot.
func NewWindow(capacity int) *Window {
	if capacity <= 0 {
		capacity = 1
	}
	return &Window{
		capacity: capacity,
		slots:    make([]*rtp.Packet, capacity),
		filled:   make([]bool, capacity),
		hist:     make([]bool, 2*capacity),
		histOff:  -int32(capacity),
	}
}

// Push accepts an already checksum-verified packet. It returns a DropKind
// (and dropped=true) for duplicates and for packets that cannot be ordered
// in because they are behind the head or farther away than the window.
func (w *Window) Push(pkt *rtp.Packet) (dropped bool, kind DropKind) {
	w.stats.Received++
	if !w.started {
		w.base = pkt.SequenceNumber
		w.started = true
	}
	idx := seqDistance(w.base, pkt.SequenceNumber)
	switch {
	case idx < 0:
		// Behind the playout head: a duplicate if we observed or skipped it,
		// otherwise a late packet that can never be ordered in.
		if w.inHistory(idx) {
			w.stats.Duplicates++
			return true, DropDuplicate
		}
		w.stats.LateDropped++
		return true, DropLate
	case int(idx) >= w.capacity:
		w.stats.LateDropped++
		return true, DropLate
	}
	slot := int(idx)
	if w.filled[slot] {
		w.stats.Duplicates++
		return true, DropDuplicate
	}
	w.slots[slot] = pkt
	w.filled[slot] = true
	if held := w.Held(); held > w.stats.PeakHeld {
		w.stats.PeakHeld = held
	}
	return false, ""
}

// inHistory reports whether relative position idx (idx <= 0) was played,
// skipped or buffered before rotating out of the history window.
func (w *Window) inHistory(idx int32) bool {
	pos := idx - w.histOff
	if pos < 0 || pos >= int32(len(w.hist)) {
		return false
	}
	return w.hist[pos]
}

func (w *Window) markHistory(rel int32) {
	pos := rel - w.histOff
	if pos >= 0 && pos < int32(len(w.hist)) {
		w.hist[pos] = true
	}
}

// Held returns how many packets are currently buffered.
func (w *Window) Held() int {
	n := 0
	for _, f := range w.filled {
		if f {
			n++
		}
	}
	return n
}

// Flush emits the contiguous run starting at the playout head and stops at
// the first hole. It never skips missing sequence numbers itself; gap
// advancement is an explicit Recover decision so that no frame is fabricated.
func (w *Window) Flush(onSample func(*rtp.Packet) error) error {
	if !w.started {
		return nil
	}
	for w.filled[0] {
		pkt := w.slots[0]
		w.slots[0] = nil
		w.filled[0] = false
		if err := onSample(pkt); err != nil {
			return err
		}
		w.stats.Emitted++
		w.rotate(true)
	}
	return nil
}

// SkipHead advances the playout head over one confirmed-missing sequence
// number. The missing seq is recorded in history so a later arrival counts
// as a duplicate rather than a re-ordered packet.
func (w *Window) SkipHead() {
	if !w.started || w.filled[0] {
		return
	}
	w.rotate(true)
}

// rotate moves the ring forward by one position, sliding the history bitmap.
// A true mark records either an observed emission or an explicitly skipped
// hole; either way a later packet with that seq is a duplicate.
func (w *Window) rotate(mark bool) {
	if mark {
		w.markHistory(0)
	}
	// uint16 addition wraps 65535 -> 0 naturally.
	w.base++
	copy(w.slots, w.slots[1:])
	copy(w.filled, w.filled[1:])
	w.slots[w.capacity-1] = nil
	w.filled[w.capacity-1] = false

	copy(w.hist, w.hist[1:])
	w.hist[len(w.hist)-1] = false
}

// Head returns the current playout-head sequence number.
func (w *Window) Head() (uint16, bool) { return w.base, w.started }

// FirstFilled returns the relative offset of the first buffered packet from
// the playout head, or -1 when the window is empty.
func (w *Window) FirstFilled() int {
	for i, f := range w.filled {
		if f {
			return i
		}
	}
	return -1
}

// Stats returns a snapshot of the window counters.
func (w *Window) Stats() Stats { return w.stats }
