package rtp

import (
	"errors"
	"fmt"
	"sort"
)

// Reorder window limits.
const (
	MinWindow = 1
	MaxWindow = 4096
	// MaxBufferedPackets bounds jitter buffer memory independently of Window.
	MaxBufferedPackets = 8192
	// MaxHoldTicks caps the configured playout hold at two seconds of 90 kHz.
	MaxHoldTicks int64 = 180000
)

var (
	// ErrInvalidWindow reports a jitter window outside [MinWindow, MaxWindow].
	ErrInvalidWindow = errors.New("rtp: invalid reorder window")
	// ErrInvalidHold reports a hold outside [0, MaxHoldTicks].
	ErrInvalidHold = errors.New("rtp: invalid jitter hold")
)

// Config configures the jitter buffer. Window is the maximum distance, in
// sequence numbers, between the emission point or the highest seen sequence and
// an accepted packet. HoldTicks is the playout delay in unified 90 kHz ticks:
// a packet is never released earlier than arrival+HoldTicks, which gives a
// reordered packet time to arrive before its slot is declared missing.
type Config struct {
	Window    int
	HoldTicks int64
}

// Packet is one received RTP packet with its restricted payload.
type Packet struct {
	Seq     uint16
	TS      uint32
	SSRC    uint32
	Payload []byte
}

// PushStatus classifies the outcome of offering a packet to the jitter buffer.
type PushStatus int

const (
	// StatusAccepted means the packet entered the buffer.
	StatusAccepted PushStatus = iota
	// StatusDuplicate means the sequence was already pending or emitted.
	StatusDuplicate
	// StatusLate means the sequence is behind the emission point and was dropped.
	StatusLate
	// StatusOutOfWindow means the sequence is too far from the buffer to reorder.
	StatusOutOfWindow
	// StatusBufferFull means the hard memory bound was reached.
	StatusBufferFull
)

func (s PushStatus) String() string {
	switch s {
	case StatusAccepted:
		return "accepted"
	case StatusDuplicate:
		return "duplicate"
	case StatusLate:
		return "late"
	case StatusOutOfWindow:
		return "out_of_window"
	case StatusBufferFull:
		return "buffer_full"
	default:
		return fmt.Sprintf("status(%d)", int(s))
	}
}

// SeqRange is an inclusive RTP sequence range. Wrapped reports that the range
// crosses the 16-bit sequence wrap (Start > End numerically).
type SeqRange struct {
	Start   uint16
	End     uint16
	Count   int
	Wrapped bool
}

// String renders the range for terminal diagnostics.
func (r SeqRange) String() string {
	return fmt.Sprintf("[%d..%d]x%d", r.Start, r.End, r.Count)
}

// Contains reports whether seq lies in the range, respecting wrap.
func (r SeqRange) Contains(seq uint16) bool {
	if r.Wrapped {
		return seq >= r.Start || seq <= r.End
	}
	return seq >= r.Start && seq <= r.End
}

// extRange converts an inclusive extended sequence span into a SeqRange.
func extRange(start, end int64) SeqRange {
	return SeqRange{Start: uint16(start), End: uint16(end), Count: int(end - start + 1), Wrapped: uint16(start) > uint16(end)}
}

// PushResult is returned for every offered packet.
type PushResult struct {
	Status PushStatus
}

// ReorderStats are observational counters, also used by the acceptance demo.
type ReorderStats struct {
	Received     uint64
	Accepted     uint64
	Emitted      uint64
	Reordered    uint64
	Duplicates   uint64
	LateDropped  uint64
	OutOfWindow  uint64
	Buffered     uint64
	PeakBuffered uint64
	GapsDeclared uint64
	GapPackets   uint64
}

type held struct {
	pkt     Packet
	arrival int64
}

// ReorderBuffer reorders packets inside a configurable window. It never emits
// a packet twice and never invents packets: missing sequences become explicit
// gaps handed to the recovery layer.
type ReorderBuffer struct {
	cfg      Config
	started  bool
	next     int64 // next extended sequence expected at the emission point
	ref      int64 // highest extended sequence seen
	pending  map[int64]held
	emitted  map[int64]struct{}
	history  []Packet // last emitted packets, bounded by Window
	gapOpen  bool     // a due gap at the emission point was already reported
	released bool     // at least one packet has been emitted upstream
	stats    ReorderStats
}

// NewReorderBuffer validates cfg and creates an empty buffer.
func NewReorderBuffer(cfg Config) (*ReorderBuffer, error) {
	if cfg.Window < MinWindow || cfg.Window > MaxWindow {
		return nil, fmt.Errorf("%w: %d not in [%d,%d]", ErrInvalidWindow, cfg.Window, MinWindow, MaxWindow)
	}
	if cfg.HoldTicks < 0 || cfg.HoldTicks > MaxHoldTicks {
		return nil, fmt.Errorf("%w: %d not in [0,%d]", ErrInvalidHold, cfg.HoldTicks, MaxHoldTicks)
	}
	return &ReorderBuffer{cfg: cfg, pending: make(map[int64]held), emitted: make(map[int64]struct{})}, nil
}

// Window returns the configured window.
func (b *ReorderBuffer) Window() int { return b.cfg.Window }

// HoldTicks returns the configured playout hold.
func (b *ReorderBuffer) HoldTicks() int64 { return b.cfg.HoldTicks }

// extend maps a raw sequence onto the extended sequence line nearest to ref.
func (b *ReorderBuffer) extend(seq uint16) int64 {
	if !b.started {
		return int64(seq)
	}
	cand := (b.ref &^ 0xffff) | int64(seq)
	if cand-b.ref > 1<<15 {
		cand -= 1 << 16
	} else if b.ref-cand > 1<<15 {
		cand += 1 << 16
	}
	return cand
}

func (b *ReorderBuffer) recordHistory(p Packet) {
	max := b.cfg.Window
	if max < MinWindow {
		max = MinWindow
	}
	b.history = append(b.history, p)
	if len(b.history) > max {
		b.history = append(b.history[:0], b.history[len(b.history)-max:]...)
	}
}

// LastEmitted returns the most recently emitted packet.
func (b *ReorderBuffer) LastEmitted() (Packet, bool) {
	if len(b.history) == 0 {
		return Packet{}, false
	}
	return b.history[len(b.history)-1], true
}

func (b *ReorderBuffer) pruneEmitted() {
	if len(b.emitted) <= 4*b.cfg.Window {
		return
	}
	floor := b.next - int64(b.cfg.Window)
	for ext := range b.emitted {
		if ext < floor {
			delete(b.emitted, ext)
		}
	}
}

// Push offers a packet that arrived at the virtual time arrival (unified
// 90 kHz ticks). Emission happens later through Flush or Drain.
func (b *ReorderBuffer) Push(p Packet, arrival int64) (PushResult, error) {
	b.stats.Received++
	ext := b.extend(p.Seq)
	if !b.started {
		b.started = true
		b.next = ext
		b.ref = ext
	}
	if ext < b.next {
		if b.released {
			if _, ok := b.emitted[ext]; ok {
				b.stats.Duplicates++
				return PushResult{Status: StatusDuplicate}, nil
			}
			b.stats.LateDropped++
			return PushResult{Status: StatusLate}, nil
		}
		// Nothing has been emitted yet, so the emission point can still be
		// rebased onto an earlier packet that jitter delivered later. The
		// packet is then buffered normally, never dropped.
		b.next = ext
		b.gapOpen = false
	}
	if _, ok := b.pending[ext]; ok {
		b.stats.Duplicates++
		return PushResult{Status: StatusDuplicate}, nil
	}
	// The window bounds reorder depth relative to the highest sequence seen. A
	// hole at the emission point does not shrink it: packets behind the hole are
	// still legitimate stream data and are released once the hole is resolved.
	// Memory is bounded separately by MaxBufferedPackets.
	if ext > b.ref+int64(b.cfg.Window) {
		b.stats.OutOfWindow++
		return PushResult{Status: StatusOutOfWindow}, nil
	}
	if len(b.pending) >= MaxBufferedPackets {
		b.stats.OutOfWindow++
		return PushResult{Status: StatusBufferFull}, nil
	}
	if ext < b.ref {
		b.stats.Reordered++
	}
	if ext > b.ref {
		b.ref = ext
	}
	b.pending[ext] = held{pkt: p, arrival: arrival}
	b.stats.Accepted++
	if n := uint64(len(b.pending)); n > b.stats.PeakBuffered {
		b.stats.PeakBuffered = n
	}
	b.stats.Buffered = uint64(len(b.pending))
	return PushResult{Status: StatusAccepted}, nil
}

// FlushResult carries packets released by Flush and any gap that became due.
type FlushResult struct {
	Ready  []Packet
	DueGap SeqRange
	HasGap bool
}

// Flush releases every contiguous packet whose playout hold has elapsed at
// virtual time now. If the emission point is a hole and a later packet has
// waited out its hold, the hole is reported once as a due gap; the caller then
// drives retransmission and either pushes the missing packet or calls SkipGap.
func (b *ReorderBuffer) Flush(now int64) FlushResult {
	var res FlushResult
	if !b.started {
		return res
	}
	res.Ready = b.releaseContiguous(now)
	if res.Ready != nil {
		b.gapOpen = false
	}
	if low, ok := b.minPending(); ok && low > b.next {
		if h, ok := b.pending[low]; ok && h.arrival+b.cfg.HoldTicks <= now {
			if !b.gapOpen {
				b.gapOpen = true
				res.DueGap = extRange(b.next, low-1)
				res.HasGap = true
			}
		}
	}
	return res
}

func (b *ReorderBuffer) releaseContiguous(now int64) []Packet {
	var out []Packet
	for {
		h, ok := b.pending[b.next]
		if !ok || h.arrival+b.cfg.HoldTicks > now {
			break
		}
		delete(b.pending, b.next)
		b.acceptEmitted(h.pkt)
		b.next++
		b.released = true
		out = append(out, h.pkt)
	}
	if len(out) > 0 {
		b.stats.Buffered = uint64(len(b.pending))
		b.pruneEmitted()
	}
	return out
}

func (b *ReorderBuffer) acceptEmitted(p Packet) {
	b.emitted[b.next] = struct{}{}
	b.stats.Emitted++
	b.recordHistory(p)
}

// PendingGap reports the hole at the emission point: the span from the next
// expected sequence to the lowest buffered sequence, when the buffer holds
// later packets but not the next one.
func (b *ReorderBuffer) PendingGap() (SeqRange, bool) {
	if !b.started {
		return SeqRange{}, false
	}
	low, ok := b.minPending()
	if !ok || low <= b.next {
		return SeqRange{}, false
	}
	return extRange(b.next, low-1), true
}

// NextPacketAfter returns the buffered packet with the lowest extended
// sequence, used to bound a declared gap with its successor's timestamp.
func (b *ReorderBuffer) NextPacketAfter() (Packet, bool) {
	low, ok := b.minPending()
	if !ok {
		return Packet{}, false
	}
	return b.pending[low].pkt, true
}

func (b *ReorderBuffer) minPending() (int64, bool) {
	first := true
	var min int64
	for ext := range b.pending {
		if first || ext < min {
			min = ext
			first = false
		}
	}
	return min, !first
}

// SkipGap force-advances the emission point past the pending hole and releases
// whatever became contiguous. The gap is recorded as declared, never filled.
func (b *ReorderBuffer) SkipGap(now int64) ([]Packet, SeqRange, bool) {
	low, ok := b.minPending()
	if !ok || low <= b.next {
		return nil, SeqRange{}, false
	}
	gap := extRange(b.next, low-1)
	b.stats.GapsDeclared++
	b.stats.GapPackets += uint64(gap.Count)
	b.next = low
	b.gapOpen = false
	ready := b.releaseContiguous(now)
	if len(ready) > 0 {
		b.released = true
	}
	return ready, gap, true
}

// Drain flushes the buffer at end of stream, emitting remaining packets in
// sequence order and returning the gaps that were never filled. It does not
// fabricate media for the missing sequences.
func (b *ReorderBuffer) Drain() ([]Packet, []SeqRange) {
	if !b.started {
		return nil, nil
	}
	exts := make([]int64, 0, len(b.pending))
	for ext := range b.pending {
		exts = append(exts, ext)
	}
	sort.Slice(exts, func(i, j int) bool { return exts[i] < exts[j] })
	var out []Packet
	var gaps []SeqRange
	for _, ext := range exts {
		if ext > b.next {
			gap := extRange(b.next, ext-1)
			gaps = append(gaps, gap)
			b.stats.GapsDeclared++
			b.stats.GapPackets += uint64(gap.Count)
			b.next = ext
		}
		h := b.pending[ext]
		delete(b.pending, ext)
		b.acceptEmitted(h.pkt)
		b.next = ext + 1
		b.released = true
		out = append(out, h.pkt)
	}
	b.stats.Buffered = 0
	b.gapOpen = false
	return out, gaps
}

// Buffered returns the number of queued packets.
func (b *ReorderBuffer) Buffered() int { return len(b.pending) }

// Stats returns a snapshot of the counters.
func (b *ReorderBuffer) Stats() ReorderStats { return b.stats }
