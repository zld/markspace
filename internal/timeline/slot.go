package timeline

import (
	"sort"

	"avcheck/internal/media"
)

// StreamComposer composes one output index at a time as frames arrive from
// several tracks. It is the streaming counterpart of Composer: instead of
// holding every frame of every track, it releases a slot as soon as every
// track has either delivered a later sequence number or been marked done.
//
// A slot is ready when, for every track, done is set or MaxSeq > index. The
// reorderer only emits committed sequence numbers, so a later number proves
// that the slot can no longer be filled. Pending frames are therefore bounded
// by the transport lookahead; MaxPending is a hard guard against a stalled or
// misbehaving consumer.
type StreamComposer struct {
	clock media.Clock
	ids   []uint32
	lo    uint64
	hi    uint64

	next    uint64
	started bool

	pending map[uint32]map[uint64]*media.Frame
	maxSeq  map[uint32]uint64
	seen    map[uint32]bool
	done    map[uint32]bool
	states  map[uint32]TrackState

	prev     media.Rat
	havePrev bool

	maxPending  int
	pendingNow  int
	PendingPeak int
}

// NewStreamComposer builds a streaming composer for the inclusive frame span
// [lo, hi]. Tracks are sorted by id for deterministic source ordering.
func NewStreamComposer(clock media.Clock, ids []uint32, lo, hi uint64, maxPending int) (*StreamComposer, error) {
	if len(ids) == 0 {
		return nil, media.NewError(media.KindRange, 0, 0, -1, "slot composer needs at least one track")
	}
	if lo > hi {
		return nil, media.NewError(media.KindRange, 0, lo, -1, "slot span [%d,%d] inverted", lo, hi)
	}
	if maxPending < 1 {
		maxPending = 1
	}
	s := &StreamComposer{
		clock:       clock,
		ids:         append([]uint32(nil), ids...),
		lo:          lo,
		hi:          hi,
		pending:     make(map[uint32]map[uint64]*media.Frame, len(ids)),
		maxSeq:      make(map[uint32]uint64, len(ids)),
		seen:        make(map[uint32]bool, len(ids)),
		done:        make(map[uint32]bool, len(ids)),
		states:      make(map[uint32]TrackState, len(ids)),
		maxPending:  maxPending,
		pendingNow:  0,
		PendingPeak: 0,
	}
	sort.Slice(s.ids, func(i, j int) bool { return s.ids[i] < s.ids[j] })
	for i := 1; i < len(s.ids); i++ {
		if s.ids[i] == s.ids[i-1] {
			return nil, media.NewError(media.KindRange, s.ids[i], 0, -1, "duplicate track id")
		}
	}
	for _, id := range s.ids {
		s.pending[id] = map[uint64]*media.Frame{}
	}
	return s, nil
}

func (s *StreamComposer) known(id uint32) bool {
	_, ok := s.pending[id]
	return ok
}

// SetState records the current recovery state of a track; sources emitted
// after this call report it.
func (s *StreamComposer) SetState(id uint32, st TrackState) error {
	if !s.known(id) {
		return media.NewError(media.KindRange, id, 0, -1, "unknown track")
	}
	s.states[id] = st
	return nil
}

// Offer buffers one frame. Sequence numbers must be strictly increasing per
// track and must not fall behind the last composed slot.
func (s *StreamComposer) Offer(id uint32, f *media.Frame) error {
	if f == nil {
		return media.NewError(media.KindState, id, 0, -1, "nil frame")
	}
	if !s.known(id) {
		return media.NewError(media.KindRange, id, f.Seq, -1, "unknown track")
	}
	if s.done[id] {
		return media.NewError(media.KindState, id, f.Seq, -1, "track already finalized")
	}
	if s.started && f.Seq < s.next {
		return media.NewError(media.KindState, id, f.Seq, -1, "slot %d already committed", s.next)
	}
	if _, dup := s.pending[id][f.Seq]; dup {
		return media.NewError(media.KindState, id, f.Seq, -1, "duplicate frame sequence")
	}
	s.pending[id][f.Seq] = f
	s.pendingNow++
	if s.pendingNow > s.PendingPeak {
		s.PendingPeak = s.pendingNow
	}
	if s.pendingNow > s.maxPending {
		return media.NewError(media.KindRange, id, f.Seq, int64(s.pendingNow),
			"compose lookahead exceeded %d frames", s.maxPending)
	}
	if !s.seen[id] || f.Seq > s.maxSeq[id] {
		s.maxSeq[id] = f.Seq
	}
	s.seen[id] = true
	return nil
}

// TrackDone announces that no further frames will arrive for a track. The
// remaining slots can then be composed with placeholders for it.
func (s *StreamComposer) TrackDone(id uint32) error {
	if !s.known(id) {
		return media.NewError(media.KindRange, id, 0, -1, "unknown track")
	}
	s.done[id] = true
	return nil
}

// AllDone reports whether every track has been finalized.
func (s *StreamComposer) AllDone() bool {
	for _, id := range s.ids {
		if !s.done[id] {
			return false
		}
	}
	return true
}

// Ready reports whether the next slot can be decided. The first call pins the
// cursor to lo.
func (s *StreamComposer) Ready() bool {
	if !s.started {
		s.next = s.lo
		s.started = true
	}
	if s.next > s.hi {
		return false
	}
	for _, id := range s.ids {
		if s.done[id] {
			continue
		}
		if !s.seen[id] || s.maxSeq[id] <= s.next {
			return false
		}
	}
	return true
}

// Next returns the next decided slot. ok is false when more input is needed
// or the whole span has been emitted.
func (s *StreamComposer) Next() (OutputFrame, bool, error) {
	if !s.Ready() {
		return OutputFrame{}, false, nil
	}
	of, err := s.build(s.next)
	if err != nil {
		return OutputFrame{}, false, err
	}
	for _, id := range s.ids {
		if _, ok := s.pending[id][s.next]; ok {
			delete(s.pending[id], s.next)
			s.pendingNow--
		}
	}
	s.next++
	return of, true, nil
}

// Drain calls fn for every slot that is currently ready.
func (s *StreamComposer) Drain(fn func(OutputFrame) error) error {
	for {
		of, ok, err := s.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := fn(of); err != nil {
			return err
		}
	}
}

func (s *StreamComposer) build(index uint64) (OutputFrame, error) {
	of := OutputFrame{Index: index}
	var best media.Rat
	have := false
	for _, id := range s.ids {
		src := SourceState{TrackID: id, Track: s.states[id]}
		f, ok := s.pending[id][index]
		if !ok {
			src.Frame = media.StatePlaceholder
			of.Sources = append(of.Sources, src)
			continue
		}
		src.Frame = f.State
		if of.Primary == nil {
			of.Primary = f
		}
		ft, err := s.clock.ToSeconds(f.Ticks)
		if err != nil {
			return OutputFrame{}, err
		}
		if !have || ft.Cmp(best) > 0 {
			best = ft
		}
		have = true
		of.Sources = append(of.Sources, src)
	}
	switch {
	case !have && s.havePrev:
		of.Time = s.prev
	case !have:
		of.Time = media.Rat{Num: 0, Den: 1}
	case s.havePrev && best.Cmp(s.prev) < 0:
		of.Time = s.prev
	default:
		of.Time = best
	}
	s.prev = of.Time
	s.havePrev = true
	return of, nil
}

// Flush finalizes all tracks and emits every remaining slot through fn. It is
// safe to call when all data has already been drained.
func (s *StreamComposer) Flush(fn func(OutputFrame) error) error {
	for _, id := range s.ids {
		s.done[id] = true
	}
	return s.Drain(fn)
}

// NextIndex reports the next sequence number to be composed, or hi+1 once
// the span is exhausted.
func (s *StreamComposer) NextIndex() uint64 {
	if !s.started {
		return s.lo
	}
	return s.next
}
