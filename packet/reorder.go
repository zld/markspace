package packet

// Kind identifies what a stream Event carries.
type Kind int

const (
	// KindRTP is a valid RTP packet emitted in sequence order.
	KindRTP Kind = iota
	// KindLoss reports a run of sequence numbers declared lost.
	KindLoss
)

// Event is the unified output of the Reorderer.
type Event struct {
	Kind     Kind
	ExtSeq   int64 // extended (wrap-free) sequence number, KindRTP only
	Info     RTPInfo
	LossFrom int64 // KindLoss: first lost extended sequence (inclusive)
	LossTo   int64 // KindLoss: last lost extended sequence (inclusive)
}

// ReordererConfig tunes gap and duplicate handling.
type ReordererConfig struct {
	// Window is how many sequence numbers beyond a gap the reorderer
	// waits for stragglers before declaring the gap lost.
	Window int64
}

// Reorderer resequences one SSRC's 16-bit sequence numbers into a
// monotonic extended sequence. Duplicates are dropped (their payload is
// never counted) and gaps beyond the configured window become loss
// events. Not safe for concurrent use.
type Reorderer struct {
	cfg      ReordererConfig
	init     bool
	cycles   int64 // accumulated wrap cycles, in sequence numbers
	maxSeq   uint16
	maxExt   int64 // extended value of the highest forward sequence seen
	nextEmit int64 // next extended sequence to emit
	pending  map[int64]RTPInfo

	// Dups counts dropped duplicate packets.
	Dups int
	// Reordered counts packets that arrived behind a higher sequence.
	Reordered int
}

// NewReorderer creates a Reorderer. A zero Window declares losses as
// soon as any higher sequence arrives.
func NewReorderer(cfg ReordererConfig) *Reorderer {
	return &Reorderer{cfg: cfg, pending: make(map[int64]RTPInfo)}
}

// extend maps a 16-bit sequence number onto the wrap-free int64 line.
func (r *Reorderer) extend(seq uint16) int64 {
	if !r.init {
		r.init = true
		r.maxSeq = seq
		r.maxExt = int64(seq)
		r.nextEmit = r.maxExt
		return r.maxExt
	}
	d := int16(seq - r.maxSeq) // wrap-aware signed delta
	if d > 0 {
		if seq < r.maxSeq { // wrapped past 65535
			r.cycles += 1 << 16
		}
		r.maxSeq = seq
		r.maxExt = r.cycles + int64(seq)
		return r.maxExt
	}
	// Late or reordered packet: same cycle, unless seq > maxSeq means it
	// belongs to the previous cycle (we already wrapped forward).
	ext := r.cycles + int64(seq)
	if seq > r.maxSeq {
		ext -= 1 << 16
	}
	return ext
}

// Push feeds one parsed packet and returns any events that become
// decidable as a result, in emission order.
func (r *Reorderer) Push(info RTPInfo) []Event {
	ext := r.extend(info.Seq)
	if ext < r.nextEmit {
		r.Dups++ // already emitted: duplicate or too-late straggler
		return nil
	}
	if _, ok := r.pending[ext]; ok {
		r.Dups++
		return nil
	}
	if ext < r.maxExt {
		r.Reordered++
	}
	r.pending[ext] = info
	return r.drain()
}

// drain emits contiguous packets and declares losses for gaps that are
// provably beyond the reorder window.
func (r *Reorderer) drain() []Event {
	var out []Event
	for {
		if info, ok := r.pending[r.nextEmit]; ok {
			delete(r.pending, r.nextEmit)
			out = append(out, Event{Kind: KindRTP, ExtSeq: r.nextEmit, Info: info})
			r.nextEmit++
			continue
		}
		if r.maxExt-r.nextEmit > r.cfg.Window {
			from := r.nextEmit
			for r.maxExt-r.nextEmit > r.cfg.Window {
				if _, ok := r.pending[r.nextEmit]; ok {
					break
				}
				r.nextEmit++
			}
			out = append(out, Event{Kind: KindLoss, LossFrom: from, LossTo: r.nextEmit - 1})
			continue
		}
		return out
	}
}

// Flush drains everything still pending at end of input, emitting
// packets in order and declaring interior gaps lost. Sequence numbers
// after the last pending packet are not reported (the stream may simply
// be truncated).
func (r *Reorderer) Flush() []Event {
	var out []Event
	for len(r.pending) > 0 {
		if info, ok := r.pending[r.nextEmit]; ok {
			delete(r.pending, r.nextEmit)
			out = append(out, Event{Kind: KindRTP, ExtSeq: r.nextEmit, Info: info})
			r.nextEmit++
			continue
		}
		// Find the next pending sequence and declare the gap lost.
		lo := int64(-1)
		for ext := range r.pending {
			if ext >= r.nextEmit && (lo < 0 || ext < lo) {
				lo = ext
			}
		}
		if lo < 0 {
			break
		}
		out = append(out, Event{Kind: KindLoss, LossFrom: r.nextEmit, LossTo: lo - 1})
		r.nextEmit = lo
	}
	return out
}
