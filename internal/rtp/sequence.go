// Package rtp reconstructs packet delivery state from timestamped RTP event
// records: 16-bit sequence numbers are extended to 64 bits, reordering is
// resolved inside a bounded window, and duplicates / losses / late arrivals
// are classified without ever mutating already committed output.
package rtp

import (
	"sort"

	"github.com/pion/rtp"

	"mediasync/internal/model"
)

// Status classifies what the reconstructor decided for one arrival.
type Status string

const (
	StatusAccepted  Status = "accepted"  // buffered, not yet committed
	StatusCommitted Status = "committed" // emitted in sequence order
	StatusDuplicate Status = "duplicate" // extended seq already seen
	StatusLate      Status = "late"      // arrived after its seq was declared missing
	StatusParse     Status = "parse_error"
)

// Packet is one reconstructed RTP packet on the nanosecond timeline.
type Packet struct {
	SSRC          uint32
	ExtSequence   uint64 // 64-bit extension of the 16-bit RTP sequence number
	Sequence      uint16
	RTPTimestamp  uint32
	ArrivalNS     model.Ticks
	ScheduleNS    model.Ticks // arrival mapped onto the RTP clock
	PayloadLen    int
	Marker        bool
}

// Outcome is returned for every fed event. Committed is non-empty only when
// the arrival (or a flush) released one or more in-order packets.
type Outcome struct {
	Status    Status
	Packet    *Packet
	Committed []Packet
	// GapExts are extended sequences declared missing by this call.
	GapExts   []uint64
}

// Reconstructor keeps per-SSRC state. Every method is safe only for
// single-goroutine use, which matches the streaming runner.
type Reconstructor struct {
	window        int
	clockHz       uint32
	ssrc          uint32
	started       bool
	baseExt       uint64 // extended seq of the first accepted packet
	highestExt    uint64 // highest extended seq ever observed
	nextCommitExt uint64 // next extended seq that may be committed
	pending       map[uint64]Packet
	seen          map[uint64]struct{}
	missing       map[uint64]struct{}
	duplicates    int
	late          int
}

// NewReconstructor builds a reconstructor bounded by reorderWindow packets.
// clockHz is the RTP timestamp frequency used to map clock units to
// nanoseconds (typical video clock is 90000 Hz).
func NewReconstructor(ssrc uint32, reorderWindow int, clockHz uint32) (*Reconstructor, error) {
	if reorderWindow <= 0 || reorderWindow > model.MaxReorderWindow {
		return nil, model.NewError(model.ClassLimitExceeded,
			"reorder window %d outside (0,%d]", reorderWindow, model.MaxReorderWindow)
	}
	if clockHz == 0 {
		return nil, model.NewError(model.ClassInvalidTimebase, "zero RTP clock rate")
	}
	return &Reconstructor{
		window:  reorderWindow,
		clockHz: clockHz,
		ssrc:    ssrc,
		pending: make(map[uint64]Packet),
		seen:    make(map[uint64]struct{}),
		missing: make(map[uint64]struct{}),
	}, nil
}

// extend maps a 16-bit sequence number to 64 bits relative to the highest
// extended sequence seen so far, choosing the nearest wrap candidate.
func (r *Reconstructor) extend(seq uint16) uint64 {
	if !r.started {
		return uint64(seq)
	}
	prev := r.highestExt
	base := prev - (prev % (1 << 16))
	candidate := base + uint64(seq)
	forward := candidate - prev
	if forward > (1<<15) && candidate >= (1<<16) {
		candidate -= 1 << 16
	}
	if int64(prev-candidate) > (1<<15) && candidate+(1<<16) < (1<<63) {
		candidate += 1 << 16
	}
	return candidate
}

// Handle processes one arrival together with its wall-clock arrival time.
func (r *Reconstructor) Handle(pkt *rtp.Packet, arrivalNS model.Ticks) Outcome {
	if pkt == nil {
		return Outcome{Status: StatusParse}
	}
	ext := r.extend(pkt.SequenceNumber)
	if !r.started {
		r.started = true
		r.baseExt = ext
		r.highestExt = ext
		r.nextCommitExt = ext
	}

	// Already delivered or already given up on: never touch committed state.
	if _, ok := r.seen[ext]; ok {
		r.duplicates++
		return Outcome{Status: StatusDuplicate}
	}
	if _, ok := r.missing[ext]; ok {
		r.late++
		return Outcome{Status: StatusLate}
	}

	p := Packet{
		SSRC:         pkt.SSRC,
		ExtSequence:  ext,
		Sequence:     pkt.SequenceNumber,
		RTPTimestamp: pkt.Timestamp,
		ArrivalNS:    arrivalNS,
		ScheduleNS:   r.toTicks(pkt.Timestamp),
		PayloadLen:   len(pkt.Payload),
		Marker:       pkt.Marker,
	}
	r.seen[ext] = struct{}{}
	r.pending[ext] = p
	if ext > r.highestExt {
		r.highestExt = ext
	}

	out := Outcome{Status: StatusAccepted, Packet: &p}
	out.Committed, out.GapExts = r.release()
	if len(out.Committed) > 0 {
		out.Status = StatusCommitted
	}
	return out
}

// release advances the contiguous frontier, declaring gaps missing once the
// reorder window behind the highest observation is exhausted.
func (r *Reconstructor) release() (committed []Packet, gaps []uint64) {
	for r.nextCommitExt <= r.highestExt {
		ext := r.nextCommitExt
		if p, ok := r.pending[ext]; ok {
			committed = append(committed, p)
			delete(r.pending, ext)
			r.nextCommitExt++
			continue
		}
		// Keep waiting while the gap is still inside the reorder window.
		if r.highestExt-ext < uint64(r.window) {
			break
		}
		r.missing[ext] = struct{}{}
		gaps = append(gaps, ext)
		r.nextCommitExt++
	}
	return committed, gaps
}

// Flush ends the stream: every still-buffered packet is committed in order
// and every unfilled gap is declared missing.
func (r *Reconstructor) Flush() Outcome {
	var gaps []uint64
	for ext := r.nextCommitExt; ext <= r.highestExt; ext++ {
		if _, ok := r.missing[ext]; ok {
			continue
		}
		if _, ok := r.pending[ext]; !ok {
			r.missing[ext] = struct{}{}
			gaps = append(gaps, ext)
		}
	}
	keys := make([]uint64, 0, len(r.pending))
	for k := range r.pending {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	committed := make([]Packet, 0, len(keys))
	for _, k := range keys {
		committed = append(committed, r.pending[k])
	}
	r.pending = map[uint64]Packet{}
	r.nextCommitExt = r.highestExt + 1
	return Outcome{Status: StatusCommitted, Committed: committed, GapExts: gaps}
}

func (r *Reconstructor) toTicks(rtpTS uint32) model.Ticks {
	return model.Ticks(uint64(rtpTS) * uint64(model.Nano) / uint64(r.clockHz))
}

// Stats is the immutable reconstruction summary.
type Stats struct {
	SSRC        uint32 `json:"ssrc"`
	TotalSeen   int    `json:"total_seen"`
	Committed   int    `json:"committed"`
	Duplicates  int    `json:"duplicates"`
	Late        int    `json:"late"`
	Missing     int    `json:"missing"`
	BaseExtSeq  uint64 `json:"base_ext_seq"`
	HighestExt  uint64 `json:"highest_ext_seq"`
}

// Snapshot returns counters without mutating state.
func (r *Reconstructor) Snapshot(committedCount int) Stats {
	return Stats{
		SSRC:       r.ssrc,
		TotalSeen:  len(r.seen),
		Committed:  committedCount,
		Duplicates: r.duplicates,
		Late:       r.late,
		Missing:    len(r.missing),
		BaseExtSeq: r.baseExt,
		HighestExt: r.highestExt,
	}
}

// MissingExts returns a sorted copy of all declared-missing sequences.
func (r *Reconstructor) MissingExts() []uint64 {
	out := make([]uint64, 0, len(r.missing))
	for ext := range r.missing {
		out = append(out, ext)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Window reports the configured reorder window.
func (r *Reconstructor) Window() int { return r.window }

// NextCommitExt returns the next extended sequence eligible for commit.
func (r *Reconstructor) NextCommitExt() uint64 { return r.nextCommitExt }

// HighestExt returns the highest extended sequence ever observed.
func (r *Reconstructor) HighestExt() uint64 { return r.highestExt }

// BaseExt returns the extended sequence of the first packet.
func (r *Reconstructor) BaseExt() uint64 { return r.baseExt }

// SeenExts returns a sorted snapshot of every accepted extended sequence.
func (r *Reconstructor) SeenExts() []uint64 {
	out := make([]uint64, 0, len(r.seen))
	for ext := range r.seen {
		out = append(out, ext)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// HasSeen reports whether ext was already accepted (used by resume filters).
func (r *Reconstructor) HasSeen(ext uint64) bool {
	_, ok := r.seen[ext]
	return ok
}

// Restore seeds reconstructor state from a checkpoint so a resumed run
// continues from the same frontier without re-counting prior packets.
func (r *Reconstructor) Restore(baseExt, nextCommit, highest uint64, missing, seen []uint64) {
	r.started = true
	r.baseExt = baseExt
	r.highestExt = highest
	r.nextCommitExt = nextCommit
	for _, ext := range missing {
		r.missing[ext] = struct{}{}
	}
	for _, ext := range seen {
		r.seen[ext] = struct{}{}
	}
}

// MissingCount reports the current number of declared-missing sequences.
func (r *Reconstructor) MissingCount() int { return len(r.missing) }
