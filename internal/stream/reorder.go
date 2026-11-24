package stream

import (
	"sort"

	"mediapipe/internal/model"
)

const (
	statusReleased uint8 = 1
	statusSkipped  uint8 = 2
)

// Reorder emits expanded RTP packets in contiguous sequence order. Missing
// sequences are declared lost once the high-water mark is Window positions
// ahead. Late arrivals after that point are counted but not emitted.
type Reorder struct {
	Window  uint64
	Health  *Health
	next    uint64
	highest uint64
	started bool
	pending map[uint64]model.Packet
	status  map[uint64]uint8
}

func NewReorder(window int, health *Health) *Reorder {
	if window <= 0 {
		window = 64
	}
	return &Reorder{
		Window:  uint64(window),
		Health:  health,
		pending: make(map[uint64]model.Packet),
		status:  make(map[uint64]uint8),
	}
}

func (r *Reorder) countLost() {
	if r.Health != nil {
		r.Health.PacketLost++
	}
}

func (r *Reorder) countDuplicate() {
	if r.Health != nil {
		r.Health.PacketDuplicate++
	}
}

func (r *Reorder) countLate() {
	if r.Health != nil {
		r.Health.PacketLate++
	}
}

// Push accepts one expanded packet and returns any packets now ready in order.
func (r *Reorder) Push(p model.Packet) []model.Packet {
	seq := p.ExtSequence
	if !r.started {
		r.started = true
		r.next = seq
		r.highest = seq
		r.pending[seq] = p
		return r.drain()
	}

	if seq < r.next {
		switch r.status[seq] {
		case statusReleased:
			r.countDuplicate()
		case statusSkipped:
			r.countLate()
		default:
			// Old state was evicted past the bounded replay window. Such a
			// packet is outside the reorder window and must not re-enter.
			r.countLate()
		}
		return nil
	}

	if _, dup := r.pending[seq]; dup || r.status[seq] == statusReleased {
		r.countDuplicate()
		return nil
	}

	r.pending[seq] = p
	if seq > r.highest {
		r.highest = seq
	}
	return r.drain()
}

func (r *Reorder) drain() []model.Packet {
	var out []model.Packet
	for r.started {
		if p, ok := r.pending[r.next]; ok {
			delete(r.pending, r.next)
			r.status[r.next] = statusReleased
			out = append(out, p)
			r.next++
			continue
		}
		if r.highest >= r.next+r.Window {
			r.status[r.next] = statusSkipped
			r.countLost()
			r.next++
			continue
		}
		break
	}
	r.evictOldStatus()
	return out
}

// Flush releases all remaining buffered packets in sequence order. Gaps inside
// the observed range are counted as losses; an unknown tail is left to the
// reader's truncation classification.
func (r *Reorder) Flush() []model.Packet {
	if !r.started || len(r.pending) == 0 {
		return nil
	}
	seqs := make([]uint64, 0, len(r.pending))
	maxSeq := r.next
	for seq := range r.pending {
		seqs = append(seqs, seq)
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })

	var out []model.Packet
	for seq := r.next; seq <= maxSeq; seq++ {
		if p, ok := r.pending[seq]; ok {
			delete(r.pending, seq)
			r.status[seq] = statusReleased
			out = append(out, p)
		} else {
			r.status[seq] = statusSkipped
			r.countLost()
		}
	}
	r.next = maxSeq + 1
	r.evictOldStatus()
	return out
}

func (r *Reorder) evictOldStatus() {
	// Keep O(Window) status entries. The extra slack makes boundary behavior
	// explicit while still bounding memory independently of stream length.
	limit := r.Window * 4
	if limit < 128 {
		limit = 128
	}
	if uint64(len(r.status)) <= limit {
		return
	}
	cutoff := uint64(0)
	if r.next > limit/2 {
		cutoff = r.next - limit/2
	}
	for seq := range r.status {
		if seq < cutoff {
			delete(r.status, seq)
		}
	}
}
