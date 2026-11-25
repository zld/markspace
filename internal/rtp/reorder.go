// Package rtp restores order and continuity to a lossy RTP packet stream:
// 16-bit sequence numbers are extended to 64 bits, duplicates dropped,
// re-ordered packets buffered, and unfilled gaps declared (never fabricated)
// once the configurable wait window elapses.
package rtp

import (
	"errors"
	"strconv"

	"github.com/pion/rtp"

	"mediasync/internal/model"
)

// ErrBufferFull is returned when the reorder buffer reaches MaxBuffered.
var ErrBufferFull = errors.New("rtp: reorder buffer capacity exceeded")

// Packet is a received RTP packet with its extended (wrap-aware) sequence
// number and the arrival index at which it was seen.
type Packet struct {
	Raw        *rtp.Packet
	ExtSeq     uint64
	Arrival    int
	ByteOffset int64 // position in the input capture, for error records
}

// Gap describes one missing sequence range that could not be recovered.
type Gap struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"` // inclusive
	Count int    `json:"count"`
}

// ReorderConfig bounds memory and recovery behaviour.
type ReorderConfig struct {
	// WaitWindow is how many *arrivals* a gap may stay open before it is
	// declared missing. Must be > 0.
	WaitWindow int
	// MaxBuffered caps out-of-order packets held in memory.
	MaxBuffered int
}

// DefaultReorderConfig returns conservative offline defaults.
func DefaultReorderConfig() ReorderConfig {
	return ReorderConfig{WaitWindow: 32, MaxBuffered: 4096}
}

// ReorderBuffer accepts packets in arrival order and emits them in extended
// sequence order. It is single-goroutine; the pipeline drives it serially.
type ReorderBuffer struct {
	cfg        ReorderConfig
	buf        map[uint64]Packet
	arrivals   int
	base       uint64 // next sequence number expected on the output
	started    bool
	Duplicates    int    `json:"duplicates"`
	Late          int    `json:"late"`
	Emitted       int    `json:"emitted"`
	Gaps          []Gap  `json:"gaps"`
	HighWater     int    `json:"high_water"` // largest buffer occupancy observed
	MaxSeq        uint64 `json:"max_seq"`
	gapOpenArrival int   // arrivals count when the current frontier gap first appeared
	rec           *model.Recorder
}

// NewReorderBuffer builds a buffer sharing the pipeline error recorder.
func NewReorderBuffer(cfg ReorderConfig, rec *model.Recorder) *ReorderBuffer {
	if cfg.WaitWindow <= 0 {
		cfg.WaitWindow = DefaultReorderConfig().WaitWindow
	}
	if cfg.MaxBuffered <= 0 {
		cfg.MaxBuffered = DefaultReorderConfig().MaxBuffered
	}
	return &ReorderBuffer{cfg: cfg, buf: map[uint64]Packet{}, rec: rec, gapOpenArrival: -1}
}

// Push ingests one parsed packet. A duplicate (already buffered, already
// emitted, or belonging to a closed gap) is counted and discarded. The
// returned bool is false when the packet could not be retained because the
// memory cap was reached.
func (b *ReorderBuffer) Push(p Packet) (bool, error) {
	b.arrivals++
	if !b.started {
		b.base = p.ExtSeq
		b.started = true
	}
	b.MaxSeq = maxU64(b.MaxSeq, p.ExtSeq)

	// Already delivered or inside a previously declared gap: late duplicate.
	if p.ExtSeq < b.base {
		b.Late++
		return true, nil
	}
	if _, ok := b.buf[p.ExtSeq]; ok {
		b.Duplicates++
		return true, nil
	}
	if len(b.buf) >= b.cfg.MaxBuffered {
		b.rec.Add(model.ErrorRecord{Code: model.ErrLimitExceeded, Track: "rtp",
			Offset: p.ByteOffset, Message: "reorder buffer memory cap reached"})
		return false, ErrBufferFull
	}
	b.buf[p.ExtSeq] = p
	if len(b.buf) > b.HighWater {
		b.HighWater = len(b.buf)
	}
	return true, nil
}

// Drain removes and returns every contiguous packet starting at base. Gaps
// inside the wait window hold the drain; call FlushExpired once enough later
// packets have arrived to skip past them.
func (b *ReorderBuffer) Drain() []Packet {
	var out []Packet
	for {
		p, ok := b.buf[b.base]
		if !ok {
			return out
		}
		delete(b.buf, b.base)
		b.base++
		b.Emitted++
		out = append(out, p)
	}
}

// FlushExpired closes gaps whose oldest missing number has waited longer than
// WaitWindow arrivals, or any gap when force is true (end of stream). It
// records a MISSING_PACKET error per gap, advances base to the next buffered
// sequence, and returns the gaps that were closed. No media is synthesised.
func (b *ReorderBuffer) FlushExpired(force bool) []Gap {
	var closed []Gap
	for {
		if _, ok := b.buf[b.base]; ok {
			b.gapOpenArrival = -1 // frontier present; no open gap
			return closed
		}
		if b.gapOpenArrival < 0 {
			b.gapOpenArrival = b.arrivals
		}
		// Frontier is missing; find the next present sequence.
		next := b.nextPresent()
		if next == b.base {
			// Nothing buffered. At forced end-of-stream, everything up to
			// the highest seen sequence is a trailing gap.
			if force && b.started && b.base <= b.MaxSeq {
				next = b.MaxSeq + 1
			} else {
				return closed
			}
		}
		wait := b.arrivals - b.gapOpenArrival
		if !force && wait < b.cfg.WaitWindow {
			return closed
		}
		gap := Gap{Start: b.base, End: next - 1, Count: int(next - b.base)}
		b.Gaps = append(b.Gaps, gap)
		b.rec.Add(model.ErrorRecord{Code: model.ErrMissingPacket, Severity: model.SevWarn,
			Track: "rtp", Offset: -1,
			Message: gapMessage(gap)})
		b.base = next
		b.gapOpenArrival = -1
		closed = append(closed, gap)
	}
}

// nextPresent returns the smallest buffered sequence >= base, or base when
// nothing is buffered (caller interprets an empty buffer via force).
func (b *ReorderBuffer) nextPresent() uint64 {
	next := ^uint64(0)
	for seq := range b.buf {
		if seq >= b.base && seq < next {
			next = seq
		}
	}
	if next == ^uint64(0) {
		return b.base
	}
	return next
}

// MissingCount sums the packet counts of all declared gaps.
func (b *ReorderBuffer) MissingCount() int {
	n := 0
	for _, g := range b.Gaps {
		n += g.Count
	}
	return n
}

// ExtendSeq maps a 16-bit RTP sequence number to a 64-bit running value.
// prev is the previously observed extended number (or 0 with seen=false on
// the first call). A forward jump larger than half the 16-bit space is read
// as a backward wrap; a backward jump as a forward wrap.
func ExtendSeq(prev uint64, seen bool, seq16 uint16) uint64 {
	if !seen {
		return uint64(seq16)
	}
	cycle := prev &^ uint64(0xFFFF)
	pos := prev & uint64(0xFFFF)
	delta := int64(uint16(seq16)) - int64(uint16(pos))
	switch {
	case delta < -0x8000:
		// Large backward move: the 16-bit counter wrapped forward past
		// 65535 (e.g. 65535 -> 0 gives delta=-65535).
		return cycle + 0x10000 + uint64(seq16)
	case delta > 0x8000:
		// Large forward jump implies an unseen backward wrap; drop one cycle.
		if cycle >= 0x10000 {
			return cycle - 0x10000 + uint64(seq16)
		}
		return uint64(seq16)
	default:
		// Within half the space: ordinary forward move, duplicate, or a
		// modest reorder inside the same cycle.
		return cycle + uint64(seq16)
	}
}

func gapMessage(g Gap) string {
	return "unrecovered sequence gap [" + strconv.FormatUint(g.Start, 10) +
		"," + strconv.FormatUint(g.End, 10) + "] count=" + strconv.Itoa(g.Count)
}

func maxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
