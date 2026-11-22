package adapter

import (
	"github.com/pion/rtp/v2"

	"mediacore/clock"
	"mediacore/media"
)

// RTPEventKind classifies the outcome of feeding one RTP packet.
type RTPEventKind uint8

const (
	RTPQueued    RTPEventKind = iota // buffered inside the reorder window
	RTPEmitted                       // released in sequence
	RTPDuplicate                     // sequence number already seen or committed
	RTPLate                          // arrived below the committed frontier
	RTPBad                           // structurally invalid packet
)

// RTPEvent is one in-order packet released onto the unified timeline.
type RTPEvent struct {
	SSRC        uint32
	SeqExpanded int64
	TSExpanded  int64
	PTSNanos    int64
	Marker      bool
	PayloadType uint8
	Payload     []byte
	// Filled marks synthetic silence/hole-fill events produced by recovery.
	Filled bool
}

// RTPStream is a per-SSRC stateful adapter. It unwraps 16-bit sequence
// numbers and 32-bit timestamps, reorders within a bounded window and never
// lets a late or duplicate packet rewrite the committed region.
type RTPStream struct {
	ssrc        uint32
	clockRate   uint32
	seq         *clock.Unwrapper
	ts          *clock.Unwrapper
	window      int
	pending     map[int64]*rtp.Packet // expanded seq -> packet, not yet committed
	holes       []media.Interval      // expanded-seq gaps jumped over
	committedTo int64                 // highest expanded seq already emitted
	haveHead    bool
	headSeq     int64
	emitted     int64
	duplicates  int64
	late        int64
	bad         int64
}

// NewRTPStream builds a stream. window must not exceed media.MaxReorderWindow.
func NewRTPStream(ssrc uint32, clockRate uint32, window int) (*RTPStream, error) {
	if window <= 0 || window > media.MaxReorderWindow {
		return nil, media.Errorf(media.CodeLimitExceeded, 0, nil, -1,
			"reorder window %d outside (0,%d]", window, media.MaxReorderWindow)
	}
	seq, err := clock.NewSequenceUnwrapper()
	if err != nil {
		return nil, err
	}
	ts, err := clock.NewTimestampUnwrapper(clockRate)
	if err != nil {
		return nil, err
	}
	return &RTPStream{
		ssrc: ssrc, clockRate: clockRate, seq: seq, ts: ts,
		window: window, pending: map[int64]*rtp.Packet{},
		committedTo: -1,
	}, nil
}

// PushResult reports what happened to one fed packet plus any events that can
// immediately be released in order.
type PushResult struct {
	Kind   RTPEventKind
	Events []RTPEvent
}

// Push feeds one raw RTP datagram.
func (s *RTPStream) Push(buf []byte) PushResult {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(buf); err != nil || len(buf) < 12 {
		s.bad++
		return PushResult{Kind: RTPBad}
	}
	return s.pushPacket(pkt)
}

func (s *RTPStream) pushPacket(pkt *rtp.Packet) PushResult {
	expSeq, _ := s.seq.Unwrap(uint64(pkt.SequenceNumber))

	// A sequence at or below the committed frontier can never re-enter the
	// stream; a sequence already buffered is a duplicate.
	if s.haveHead && expSeq <= s.committedTo {
		s.late++
		return PushResult{Kind: RTPLate}
	}
	if _, buffered := s.pending[expSeq]; buffered {
		s.duplicates++
		return PushResult{Kind: RTPDuplicate}
	}

	if !s.haveHead {
		s.haveHead = true
		s.headSeq = expSeq
	}
	s.pending[expSeq] = pkt

	events := s.drain(false)
	kind := RTPQueued
	if len(events) > 0 {
		kind = RTPEmitted
	}
	return PushResult{Kind: kind, Events: events}
}

// drain releases in-order packets. Contiguous packets leave immediately for
// low latency; a missing sequence is held open for reordering until the
// reorder window fills (a packet at missing+window is buffered), after which
// the gap is declared a hole (reported to recovery) and the stream advances
// over it. flushing releases whatever remains.
func (s *RTPStream) drain(flushing bool) []RTPEvent {
	var out []RTPEvent
	for len(s.pending) > 0 {
		missing := s.committedTo + 1
		pkt, ok := s.pending[missing]
		if !ok {
			// The hole stays open while fewer than `window` later packets
			// have arrived; flushing ignores the wait.
			if !flushing && s.highWater() < missing+int64(s.window) {
				return out
			}
			low := s.lowestPending()
			if low > missing {
				s.holes = append(s.holes, media.Interval{Start: missing, End: low})
			}
			pkt = s.pending[low]
			missing = low
		}
		expTS, _ := s.ts.Unwrap(uint64(pkt.Timestamp))
		out = append(out, s.makeEvent(pkt, missing, expTS))
		delete(s.pending, missing)
		s.committedTo = missing
		s.emitted++
	}
	return out
}

func (s *RTPStream) highWater() int64 {
	hi := s.committedTo
	for k := range s.pending {
		if k > hi {
			hi = k
		}
	}
	return hi
}

func (s *RTPStream) lowestPending() int64 {
	low := int64(-1)
	for k := range s.pending {
		if low < 0 || k < low {
			low = k
		}
	}
	return low
}

func (s *RTPStream) makeEvent(pkt *rtp.Packet, expSeq, expTS int64) RTPEvent {
	pts, _ := media.ToNanos(expTS, media.TimeScale(s.clockRate))
	return RTPEvent{
		SSRC: s.ssrc, SeqExpanded: expSeq, TSExpanded: expTS, PTSNanos: pts,
		Marker: pkt.Marker, PayloadType: pkt.PayloadType,
		Payload: append([]byte(nil), pkt.Payload...),
	}
}

// Flush emits everything still buffered, jumping over holes. All holes
// observed up to this point (window-expiry gaps plus any final gap) are
// returned for the recovery subsystem to fill deterministically.
func (s *RTPStream) Flush() (events []RTPEvent, holes []media.Interval) {
	events = s.drain(true)
	holes = s.holes
	s.holes = nil
	return events, holes
}

// DrainHoles returns and clears the sequence gaps already declared during
// normal (non-flush) processing, for use by the recovery layer.
func (s *RTPStream) DrainHoles() []media.Interval {
	out := s.holes
	s.holes = nil
	return out
}

// Counters returns emitted/duplicate/late/bad packet totals.
func (s *RTPStream) Counters() (emitted, duplicates, late, bad int64) {
	return s.emitted, s.duplicates, s.late, s.bad
}

// CommittedTo returns the highest expanded sequence already emitted.
func (s *RTPStream) CommittedTo() int64 { return s.committedTo }

// RTPStreamState is the serialisable checkpoint of an RTP stream.
type RTPStreamState struct {
	SSRC        uint32            `json:"ssrc"`
	ClockRate   uint32            `json:"clock_rate"`
	Window      int               `json:"window"`
	SeqState    clock.UnwrapState `json:"seq_state"`
	TSState     clock.UnwrapState `json:"ts_state"`
	CommittedTo int64             `json:"committed_to"`
	HeadSeq     int64             `json:"head_seq"`
	HaveHead    bool              `json:"have_head"`
	Emitted     int64             `json:"emitted"`
	Duplicates  int64             `json:"duplicates"`
	Late        int64             `json:"late"`
	Bad         int64             `json:"bad"`
	Holes       []media.Interval  `json:"holes"`
	Pending     map[int64][]byte  `json:"pending"` // expanded seq -> marshalled packet
}

// Snapshot captures the stream state. Buffered packets are keyed by their
// expanded sequence and marshalled so the checkpoint is self-contained.
func (s *RTPStream) Snapshot() (RTPStreamState, error) {
	pending := make(map[int64][]byte, len(s.pending))
	for expSeq, pkt := range s.pending {
		raw, err := pkt.Marshal()
		if err != nil {
			return RTPStreamState{}, media.NewError(media.CodeBadPacket, 0, nil, -1, err.Error())
		}
		pending[expSeq] = raw
	}
	holes := append([]media.Interval(nil), s.holes...)
	return RTPStreamState{
		SSRC: s.ssrc, ClockRate: s.clockRate, Window: s.window,
		SeqState: s.seq.Snapshot(), TSState: s.ts.Snapshot(),
		CommittedTo: s.committedTo, HeadSeq: s.headSeq, HaveHead: s.haveHead,
		Emitted: s.emitted, Duplicates: s.duplicates, Late: s.late, Bad: s.bad,
		Holes: holes, Pending: pending,
	}, nil
}

// RestoreRTPStream rebuilds a stream from a checkpoint. Buffered packets are
// placed back under their stored expanded sequence without re-unwrapping.
func RestoreRTPStream(st RTPStreamState) (*RTPStream, error) {
	seq, err := clock.RestoreUnwrap(st.SeqState)
	if err != nil {
		return nil, err
	}
	ts, err := clock.RestoreUnwrap(st.TSState)
	if err != nil {
		return nil, err
	}
	s := &RTPStream{
		ssrc: st.SSRC, clockRate: st.ClockRate, window: st.Window,
		seq: seq, ts: ts, pending: map[int64]*rtp.Packet{},
		holes:       append([]media.Interval(nil), st.Holes...),
		committedTo: st.CommittedTo, headSeq: st.HeadSeq, haveHead: st.HaveHead,
		emitted: st.Emitted, duplicates: st.Duplicates, late: st.Late, bad: st.Bad,
	}
	for expSeq, raw := range st.Pending {
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, media.NewError(media.CodeBadPacket, st.SSRC, nil, -1, err.Error())
		}
		s.pending[expSeq] = pkt
	}
	return s, nil
}
