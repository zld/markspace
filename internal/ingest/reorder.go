package ingest

import (
	"sort"

	"medpipe/internal/clock"
)

// ReorderConfig bounds how many sequence positions the buffer waits before
// declaring a gap. The generator's reorder window is smaller than this value.
type ReorderConfig struct {
	GapThreshold uint64
}

// ReorderStats contains stable counters for manifest and CLI output.
type ReorderStats struct {
	Received     int64 `json:"received"`
	Delivered    int64 `json:"delivered"`
	Duplicates   int64 `json:"duplicates"`
	OutOfOrder   int64 `json:"out_of_order"`
	Gaps         int64 `json:"gaps"`
	MissingCount int   `json:"missing_count"`
}

type reorderStream struct {
	next        uint64
	initialized bool
	highest     uint64
	seen        map[uint64]bool
	pending     map[uint64]Item
}

// ReorderBuffer sorts RTP Items by SSRC and extended sequence number.
type ReorderBuffer struct {
	cfg     ReorderConfig
	streams map[uint32]*reorderStream
	Missing []clock.Location
	Issues  []*clock.Error
	Stats   ReorderStats
}

func NewReorderBuffer(cfg ReorderConfig) *ReorderBuffer {
	if cfg.GapThreshold == 0 {
		cfg.GapThreshold = 64
	}
	return &ReorderBuffer{cfg: cfg, streams: make(map[uint32]*reorderStream)}
}

func (b *ReorderBuffer) stream(ssrc uint32) *reorderStream {
	st := b.streams[ssrc]
	if st == nil {
		st = &reorderStream{seen: make(map[uint64]bool), pending: make(map[uint64]Item)}
		b.streams[ssrc] = st
	}
	return st
}

// Expect sets the first contiguous extended sequence for an SSRC.
func (b *ReorderBuffer) Expect(ssrc uint32, extSeq uint64) {
	st := b.stream(ssrc)
	if !st.initialized {
		st.next = extSeq
		st.highest = extSeq
		st.initialized = true
	}
}

// Submit accepts one parsed RTP item and returns any newly contiguous items.
// Duplicates return no items and are counted only once.
func (b *ReorderBuffer) Submit(item Item) []Item {
	if item.Kind != "rtp" {
		return nil
	}
	b.Stats.Received++
	st := b.stream(item.SSRC)
	if !st.initialized {
		st.next = item.ExtSeq
		st.highest = item.ExtSeq
		st.initialized = true
	}
	if item.ExtSeq > st.highest {
		st.highest = item.ExtSeq
	}

	if st.seen[item.ExtSeq] {
		b.Stats.Duplicates++
		b.addIssue(clock.CodeDuplicate, item, "duplicate RTP packet ignored")
		return nil
	}
	st.seen[item.ExtSeq] = true

	if item.ExtSeq < st.next {
		// A packet below next can occur only after gap expiry. It is not a
		// duplicate, but order can no longer be reconstructed; record it and
		// avoid feeding it twice.
		b.Stats.OutOfOrder++
		b.addIssue(clock.CodeReorder, item, "late RTP packet arrived after gap was skipped")
		return nil
	}
	if item.ExtSeq > st.next {
		b.Stats.OutOfOrder++
		b.addIssue(clock.CodeReorder, item, "RTP packet arrived out of sequence order")
		st.pending[item.ExtSeq] = item
	} else {
		st.pending[item.ExtSeq] = item
	}

	b.expireGaps(item.SSRC, st)
	return b.drain(st)
}

func (b *ReorderBuffer) expireGaps(ssrc uint32, st *reorderStream) {
	for {
		if _, ok := st.pending[st.next]; ok {
			return
		}
		if st.highest < st.next+b.cfg.GapThreshold {
			return
		}
		seq := uint16(st.next & 0xffff)
		loc := clock.Location{
			SSRC: ssrc, Sequence: seq, ExtendedSequence: st.next,
			ByteOffset: -1, PacketError: 1,
		}
		b.Missing = append(b.Missing, loc)
		b.Stats.Gaps++
		b.Stats.MissingCount++
		b.Issues = append(b.Issues, clock.Errorf(clock.CodeGap, &loc, "missing RTP sequence %d", seq))
		st.next++
	}
}

func (b *ReorderBuffer) drain(st *reorderStream) []Item {
	var out []Item
	for {
		item, ok := st.pending[st.next]
		if !ok {
			break
		}
		delete(st.pending, st.next)
		out = append(out, item)
		b.Stats.Delivered++
		st.next++
	}
	return out
}

// Flush records all remaining gaps and delivers buffered items in order.
func (b *ReorderBuffer) Flush() []Item {
	var out []Item
	ssrcs := make([]uint32, 0, len(b.streams))
	for ssrc := range b.streams {
		ssrcs = append(ssrcs, ssrc)
	}
	sort.Slice(ssrcs, func(i, j int) bool { return ssrcs[i] < ssrcs[j] })
	for _, ssrc := range ssrcs {
		st := b.streams[ssrc]
		for len(st.pending) > 0 {
			if _, ok := st.pending[st.next]; !ok {
				seq := uint16(st.next & 0xffff)
				loc := clock.Location{SSRC: ssrc, Sequence: seq, ExtendedSequence: st.next, ByteOffset: -1, PacketError: 1}
				b.Missing = append(b.Missing, loc)
				b.Stats.Gaps++
				b.Stats.MissingCount++
				b.Issues = append(b.Issues, clock.Errorf(clock.CodeGap, &loc, "missing RTP sequence %d at flush", seq))
				st.next++
				continue
			}
			out = append(out, b.drain(st)...)
		}
	}
	return out
}

func (b *ReorderBuffer) addIssue(code clock.ErrorCode, item Item, msg string) {
	loc := &clock.Location{
		SSRC: item.SSRC, Sequence: item.Sequence, ExtendedSequence: item.ExtSeq,
		Timestamp: item.Timestamp, ByteOffset: item.Offset,
	}
	b.Issues = append(b.Issues, clock.Errorf(code, loc, "%s", msg))
}

// ReorderState is the checkpointable representation.
type ReorderState struct {
	Streams map[uint32]ReorderStreamState `json:"streams"`
	Missing []clock.Location              `json:"missing,omitempty"`
	Stats   ReorderStats                  `json:"stats"`
}

type ReorderStreamState struct {
	Next        uint64          `json:"next"`
	Initialized bool            `json:"initialized"`
	Highest     uint64          `json:"highest"`
	Seen        []uint64        `json:"seen"`
	Pending     map[uint64]Item `json:"pending,omitempty"`
}

func (b *ReorderBuffer) State() ReorderState {
	rs := ReorderState{Streams: make(map[uint32]ReorderStreamState), Missing: append([]clock.Location(nil), b.Missing...), Stats: b.Stats}
	for ssrc, st := range b.streams {
		s := ReorderStreamState{Next: st.next, Initialized: st.initialized, Highest: st.highest}
		for ext := range st.seen {
			s.Seen = append(s.Seen, ext)
		}
		sort.Slice(s.Seen, func(i, j int) bool { return s.Seen[i] < s.Seen[j] })
		if len(st.pending) > 0 {
			s.Pending = make(map[uint64]Item, len(st.pending))
			for k, v := range st.pending {
				s.Pending[k] = v
			}
		}
		rs.Streams[ssrc] = s
	}
	return rs
}

func (b *ReorderBuffer) Restore(rs ReorderState) {
	b.Stats = rs.Stats
	b.Missing = append([]clock.Location(nil), rs.Missing...)
	b.streams = make(map[uint32]*reorderStream)
	for ssrc, ss := range rs.Streams {
		st := &reorderStream{next: ss.Next, initialized: ss.Initialized, highest: ss.Highest,
			seen: make(map[uint64]bool), pending: make(map[uint64]Item)}
		for _, ext := range ss.Seen {
			st.seen[ext] = true
		}
		for ext, item := range ss.Pending {
			st.pending[ext] = item
		}
		b.streams[ssrc] = st
	}
}
