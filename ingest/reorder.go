package ingest

import (
	"sort"

	"mediacore/media"
)

// ReorderConfig controls how many packets may be buffered while waiting for a
// hole to fill. Window is measured in packets regardless of wall time so the
// output is fully deterministic on every run.
type ReorderConfig struct {
	// Window is the maximum distance, in packets, that the highest seen
	// sequence may run ahead of the next expected sequence before a gap is
	// declared and processing continues.
	Window int
	// LateHistory bounds how many gap ranges are retained for classifying
	// late arrivals. Zero means use the default.
	LateHistory int
}

// DefaultReorderConfig returns a 64-packet window.
func DefaultReorderConfig() ReorderConfig {
	return ReorderConfig{Window: 64, LateHistory: 1024}
}

// Validate reports configuration errors with the shared error model.
func (c ReorderConfig) Validate() error {
	if c.Window <= 0 {
		return media.New(media.CatInvalidParams, media.StageIngest, "reorder window must be positive")
	}
	if c.LateHistory < 0 {
		return media.New(media.CatInvalidParams, media.StageIngest, "late history cannot be negative")
	}
	return nil
}

// Gap is an explicit run of missing sequence numbers.
type Gap struct {
	StreamID uint32 `json:"stream_id"`
	FromExt  int64  `json:"from_ext"`
	ToExt    int64  `json:"to_ext"`
	FromSeq  uint16 `json:"from_seq"`
	ToSeq    uint16 `json:"to_seq"`
	Count    int    `json:"count"`
}

// Result is the ordered ingest output.
type Result struct {
	Packets    []*Packet
	Gaps       []Gap
	Duplicates int
	Late       int
}

// MissingFrames is the total number of sequence slots declared missing.
func (r Result) MissingFrames() int {
	total := 0
	for _, g := range r.Gaps {
		total += g.Count
	}
	return total
}

// Reorder deduplicates and reorders packets per stream. Not safe for
// concurrent use.
type Reorder struct {
	cfg     ReorderConfig
	streams map[uint32]*streamState
	order   []uint32
}

type streamState struct {
	id     uint32
	window int
	init   bool
	// rawRef/extRef is a reference point: the most recently observed new
	// sequence and its extended index. New packets extend from it with 16-bit
	// arithmetic, so the timeline keeps working after arbitrary wraps.
	rawRef   uint16
	extRef   int64
	highest  int64 // highest extended sequence observed
	next     int64 // next extended sequence to release
	pending  map[int64]*Packet
	ordered  []*Packet
	gaps     []Gap
	gapLimit int
	dups     int
	late     int
}

// NewReorder constructs a reorder stage, clamping invalid configuration.
func NewReorder(cfg ReorderConfig) *Reorder {
	def := DefaultReorderConfig()
	if cfg.Window <= 0 {
		cfg.Window = def.Window
	}
	if cfg.LateHistory <= 0 {
		cfg.LateHistory = def.LateHistory
	}
	return &Reorder{cfg: cfg, streams: make(map[uint32]*streamState)}
}

func (r *Reorder) state(id uint32) *streamState {
	st := r.streams[id]
	if st == nil {
		st = &streamState{
			id:       id,
			window:   r.cfg.Window,
			pending:  make(map[int64]*Packet),
			gapLimit: r.cfg.LateHistory,
		}
		r.streams[id] = st
		r.order = append(r.order, id)
	}
	return st
}

func (st *streamState) rawAt(ext int64) uint16 {
	return uint16(int64(st.rawRef) - (st.extRef - ext))
}

// inGap reports whether ext fell inside a gap declared recently enough to
// still classify an arriving packet as late.
func (st *streamState) inGap(ext int64) bool {
	for i := len(st.gaps) - 1; i >= 0; i-- {
		g := st.gaps[i]
		if ext >= g.FromExt && ext <= g.ToExt {
			return true
		}
		if ext > g.ToExt {
			// Gaps are appended in increasing order, so nothing older matters.
			return false
		}
	}
	return false
}

// Add feeds one packet and returns the packets released in order by this call.
// Duplicates are suppressed, packets that arrive after their hole was declared
// are counted as late, and unrecoverable holes become explicit gaps.
func (r *Reorder) Add(p *Packet) []*Packet {
	st := r.state(p.StreamID)
	if !st.init {
		// The first arriving packet anchors the timeline at extended index 0.
		// If it is out of order, the re-anchor path below absorbs earlier
		// packets as long as nothing has been released yet.
		st.init = true
		st.rawRef = p.Seq
		st.extRef = 0
		st.highest = 0
		st.pending[0] = p
		return st.flush(false)
	}
	ext := st.extRef + int64(int16(p.Seq-st.rawRef))
	if ext < 0 {
		if st.next == 0 {
			st.absorbEarlier(p, ext)
			return st.flush(false)
		}
		st.classifyPast(ext)
		return nil
	}
	if ext < st.next {
		st.classifyPast(ext)
		return nil
	}
	if _, exists := st.pending[ext]; exists {
		st.dups++
		return nil
	}
	st.pending[ext] = p
	if ext > st.highest {
		st.highest = ext
	}
	if ext > st.extRef {
		// Advance the reference only on newer packets; duplicates and
		// reordered stragglers must not move it backwards.
		st.extRef = ext
		st.rawRef = p.Seq
	}
	return st.flush(false)
}

// absorbEarlier accepts a packet that arrived before anything has been
// released but sits earlier than the first packet. The extended timeline is
// shifted so the earlier packet becomes index 0, preserving delivery order.
func (st *streamState) absorbEarlier(p *Packet, ext int64) {
	shift := -ext
	st.pending = shiftPending(st.pending, shift)
	st.highest += shift
	if st.highest < 0 {
		st.highest = 0
	}
	// rawRef and extRef must always name the same packet. Re-anchoring makes
	// the newly absorbed packet the reference at index 0; anything else would
	// desynchronize future 16-bit extensions and alias sequence numbers.
	st.rawRef = p.Seq
	st.extRef = 0
	st.pending[0] = p
}

// classifyPast counts a packet whose extended index is already behind the
// delivery frontier. Inside a declared gap it is late; otherwise that slot was
// already released, so it is a duplicate.
func (st *streamState) classifyPast(ext int64) {
	if st.inGap(ext) {
		st.late++
	} else {
		st.dups++
	}
}

func shiftPending(in map[int64]*Packet, shift int64) map[int64]*Packet {
	out := make(map[int64]*Packet, len(in))
	for k, v := range in {
		out[k+shift] = v
	}
	return out
}

// flush releases contiguous packets. While the window is not yet satisfied it
// holds the leading run so a late-arriving earlier packet can still be
// absorbed in order. When force is set (end of stream) everything pending is
// released and every remaining hole becomes an explicit gap.
func (st *streamState) flush(force bool) []*Packet {
	var out []*Packet
	for {
		if p, ok := st.pending[st.next]; ok {
			// At the very front of the stream we cannot know whether an
			// earlier packet will still arrive, so hold the head until the
			// window fills or the stream ends.
			if !force && st.next == 0 && st.highest-st.next < int64(st.window) {
				break
			}
			delete(st.pending, st.next)
			st.next++
			st.ordered = append(st.ordered, p)
			out = append(out, p)
			continue
		}
		if len(st.pending) == 0 {
			break
		}
		if !force && st.highest-st.next < int64(st.window) {
			break
		}
		next := st.minPending()
		st.recordGap(next)
		st.next = next
	}
	return out
}

func (st *streamState) minPending() int64 {
	first := true
	var min int64
	for ext := range st.pending {
		if first || ext < min {
			min = ext
			first = false
		}
	}
	return min
}

func (st *streamState) recordGap(to int64) {
	if to <= st.next {
		return
	}
	from := st.next
	st.gaps = append(st.gaps, Gap{
		StreamID: st.id,
		FromExt:  from,
		ToExt:    to - 1,
		FromSeq:  st.rawAt(from),
		ToSeq:    st.rawAt(to - 1),
		Count:    int(to - from),
	})
	st.trimGaps()
}

// trimGaps bounds the retained gap history used for late classification.
func (st *streamState) trimGaps() {
	if st.gapLimit <= 0 || len(st.gaps) <= st.gapLimit {
		return
	}
	st.gaps = st.gaps[len(st.gaps)-st.gapLimit:]
}

// Finalize delivers all remaining buffered packets, emitting gaps for holes,
// and returns a Result whose packets are grouped by ascending stream id and
// ordered by extended sequence within each stream.
func (r *Reorder) Finalize() (Result, error) {
	res := Result{}
	ids := append([]uint32(nil), r.order...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		st := r.streams[id]
		st.flush(true)
		res.Packets = append(res.Packets, st.ordered...)
		res.Gaps = append(res.Gaps, st.gaps...)
		res.Duplicates += st.dups
		res.Late += st.late
	}
	return res, nil
}

// ProcessPacketStream parses and reorders every packet in buf.
func ProcessPacketStream(buf []byte, cfg ReorderConfig) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	pr := NewPacketReader(buf)
	ro := NewReorder(cfg)
	for {
		p, ok, err := pr.Next()
		if err != nil {
			return Result{}, err
		}
		if !ok {
			break
		}
		ro.Add(p)
	}
	return ro.Finalize()
}
