package jitter

import (
	"fmt"

	"medcheck/internal/clock"
)

// EventKind identifies an ordered jitter-buffer output.
type EventKind string

const (
	EventData EventKind = "data"
	EventGap  EventKind = "gap"
)

// TimedPacket is an RTP packet after clock mapping and before recovery.
type TimedPacket struct {
	Stream      clock.StreamID
	SSRC        uint32
	Seq         uint16
	ExtSeq      uint64
	PTSNS       int64
	DurationNS  int64
	ArrivalNS   int64
	Offset      int64
	Payload     []byte
	Keyframe    bool
	CodecConfig []byte
}

// Event is either an in-order packet or an expired sequence gap.
type Event struct {
	Kind   EventKind
	Stream clock.StreamID
	SSRC   uint32
	Seqs   []uint64
	Packet *TimedPacket
	PTSNS  int64
}

// Config controls deterministic packet-count and nanosecond windows.
type Config struct {
	WindowPackets int
	MaxWaitNS     int64
}

// DefaultConfig waits for up to four subsequent sequence positions.
func DefaultConfig() Config {
	return Config{WindowPackets: 4, MaxWaitNS: 40_000_000}
}

type streamState struct {
	stream    clock.StreamID
	next      uint64
	highest   uint64
	queued    map[uint64]TimedPacket
	waitSince int64
	haveWait  bool
	finalSeq  *uint64
}

// Buffer reorders packets per SSRC and emits gaps only after window expiry.
type Buffer struct {
	cfg     Config
	streams map[uint32]*streamState
	nowNS   int64
}

// New creates a jitter buffer.
func New(cfg Config) *Buffer {
	if cfg.WindowPackets <= 0 {
		cfg.WindowPackets = 4
	}
	return &Buffer{cfg: cfg, streams: make(map[uint32]*streamState)}
}

// Register binds an SSRC to a stream for gap events that have no packet.
func (b *Buffer) Register(ssrc uint32, stream clock.StreamID) {
	b.state(ssrc).stream = stream
}

// StreamSnapshot is the checkpointable state of one SSRC buffer.
type StreamSnapshot struct {
	Next      uint64                 `json:"next"`
	Highest   uint64                 `json:"highest"`
	WaitSince int64                  `json:"wait_since"`
	HaveWait  bool                   `json:"have_wait"`
	Queued    map[uint64]TimedPacket `json:"queued"`
}

// Snapshot exports all per-SSRC state.
func (b *Buffer) Snapshot() map[uint32]StreamSnapshot {
	out := make(map[uint32]StreamSnapshot, len(b.streams))
	for ssrc, st := range b.streams {
		queued := make(map[uint64]TimedPacket, len(st.queued))
		for seq, p := range st.queued {
			queued[seq] = p
		}
		out[ssrc] = StreamSnapshot{
			Next: st.next, Highest: st.highest, WaitSince: st.waitSince,
			HaveWait: st.haveWait, Queued: queued,
		}
	}
	return out
}

// Restore replaces buffer state from a checkpoint snapshot.
func (b *Buffer) Restore(in map[uint32]StreamSnapshot) {
	b.streams = make(map[uint32]*streamState, len(in))
	for ssrc, snap := range in {
		st := b.state(ssrc)
		st.next = snap.Next
		st.highest = snap.Highest
		st.waitSince = snap.WaitSince
		st.haveWait = snap.HaveWait
		st.queued = make(map[uint64]TimedPacket, len(snap.Queued))
		for seq, p := range snap.Queued {
			st.queued[seq] = p
			if st.stream == "" {
				st.stream = p.Stream
			}
		}
	}
}

// NextSeq returns the next extended sequence expected for an SSRC.
func (b *Buffer) NextSeq(ssrc uint32) (uint64, bool) {
	st := b.streams[ssrc]
	if st == nil {
		return 0, false
	}
	return st.next, true
}

// RestoreNext restores a checkpoint boundary. Packets before next are ignored.
func (b *Buffer) RestoreNext(ssrc uint32, next uint64) {
	st := b.state(ssrc)
	st.next = next
	st.highest = next - 1
}

func (b *Buffer) state(ssrc uint32) *streamState {
	st := b.streams[ssrc]
	if st == nil {
		st = &streamState{queued: make(map[uint64]TimedPacket)}
		b.streams[ssrc] = st
	}
	return st
}

// Push inserts a packet and returns all currently releasable events.
// Duplicate packets are counted in dup and never appended to events.
func (b *Buffer) Push(p TimedPacket) (events []Event, duplicate bool, err error) {
	st := b.state(p.SSRC)
	if st.stream == "" {
		st.stream = p.Stream
	}
	b.nowNS = max64(b.nowNS, p.ArrivalNS)

	if st.next != 0 || st.haveStarted() {
		if p.ExtSeq < st.next {
			return nil, true, nil
		}
		if _, ok := st.queued[p.ExtSeq]; ok {
			return nil, true, nil
		}
	} else {
		st.next = p.ExtSeq
		st.highest = p.ExtSeq
	}

	st.queued[p.ExtSeq] = p
	if p.ExtSeq > st.highest {
		st.highest = p.ExtSeq
	}
	return b.drain(p.SSRC, false), false, nil
}

func (st *streamState) haveStarted() bool {
	return st.highest != 0 || len(st.queued) > 0
}

// Advance expires time-based windows.
func (b *Buffer) Advance(nowNS int64) []Event {
	if nowNS < b.nowNS {
		nowNS = b.nowNS // clock advancement is monotonic
	}
	b.nowNS = nowNS
	var out []Event
	for ssrc := range b.streams {
		out = append(out, b.drain(ssrc, false)...)
	}
	return out
}

// SetFinalSeq records the last known sequence so tail losses can be emitted.
func (b *Buffer) SetFinalSeq(ssrc uint32, final uint64) {
	st := b.state(ssrc)
	st.finalSeq = &final
	if final > st.highest {
		st.highest = final
	}
}

// Flush emits all queued data and every remaining gap through FinalSeq/highest.
func (b *Buffer) Flush() []Event {
	var out []Event
	for ssrc := range b.streams {
		out = append(out, b.drain(ssrc, true)...)
	}
	return out
}

func (b *Buffer) drain(ssrc uint32, timeExpired bool) []Event {
	st := b.streams[ssrc]
	if st == nil {
		return nil
	}
	var out []Event
	windowExpired := st.highest >= st.next+uint64(b.cfg.WindowPackets-1)
	deadlineExpired := timeExpired || (st.haveWait && b.cfg.MaxWaitNS > 0 &&
		b.nowNS-st.waitSince >= b.cfg.MaxWaitNS)
	forceGap := windowExpired || deadlineExpired

	for st.next <= st.highest {
		if p, ok := st.queued[st.next]; ok {
			delete(st.queued, st.next)
			out = append(out, Event{Kind: EventData, Stream: p.Stream, SSRC: ssrc, Packet: &p, PTSNS: p.PTSNS})
			st.next++
			st.haveWait = false
			continue
		}
		if !forceGap {
			if !st.haveWait {
				st.waitSince = b.nowNS
				st.haveWait = true
			}
			break
		}
		seq := st.next
		out = append(out, Event{
			Kind: EventGap, Stream: st.stream, SSRC: ssrc,
			Seqs: []uint64{seq}, PTSNS: b.gapPTS(st, seq),
		})
		st.next++
	}
	if st.next > st.highest {
		st.haveWait = false
	}
	return out
}

func (b *Buffer) gapPTS(st *streamState, seq uint64) int64 {
	// Interpolate from the nearest queued packet when possible.
	for delta := uint64(1); delta <= uint64(b.cfg.WindowPackets)+1; delta++ {
		if p, ok := st.queued[seq+delta]; ok && delta > 0 {
			if p.DurationNS > 0 {
				return p.PTSNS - int64(delta)*p.DurationNS
			}
			return p.PTSNS
		}
	}
	return 0
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// String helps test failure output.
func (e Event) String() string {
	if e.Kind == EventData && e.Packet != nil {
		return fmt.Sprintf("data(%s,%d)", e.Stream, e.Packet.ExtSeq)
	}
	return fmt.Sprintf("gap(%s,%v)", e.Stream, e.Seqs)
}
