package timeline

import (
	"errors"
	"sync"

	"github.com/pion/rtp"
)

// UnwrapSeq expands a 16-bit RTP sequence number into a monotonically
// increasing uint64 counter, tolerating forward jumps and wrap-around.
// Consecutive calls must observe the same source (ssrc) stream.
type UnwrapSeq struct {
	last    uint16
	cycles  uint64
	started bool
}

// Push feeds one raw sequence number and returns its unwrapped value.
func (u *UnwrapSeq) Push(seq uint16) uint64 {
	if !u.started {
		u.started = true
		u.last = seq
		return uint64(seq)
	}
	if seq < u.last {
		// A decrease is a wrap only when the jump crosses the modulus boundary.
		if int(u.last)-int(seq) > 32768 {
			u.cycles += 1 << 16
		}
	} else if int(seq)-int(u.last) > 32768 {
		// Huge forward jump implies a backwards cycle correction.
		if u.cycles >= 1<<16 {
			u.cycles -= 1 << 16
		}
	}
	u.last = seq
	return u.cycles | uint64(seq)
}

// UnwrapTS expands a 32-bit RTP timestamp the same way.
type UnwrapTS struct {
	last    uint32
	cycles  uint64
	started bool
}

// Push feeds one raw timestamp and returns its unwrapped value.
func (u *UnwrapTS) Push(ts uint32) uint64 {
	if !u.started {
		u.started = true
		u.last = ts
		return uint64(ts)
	}
	if ts < u.last && uint32(1<<31) < u.last-ts {
		u.cycles += 1 << 32
	} else if ts > u.last && ts-u.last > 1<<31 && u.cycles >= 1<<32 {
		u.cycles -= 1 << 32
	}
	u.last = ts
	return u.cycles | uint64(ts)
}

// RTPConfig tunes the reorder buffer for one SSRC.
type RTPConfig struct {
	SSRC          uint32
	ClockRate     uint64 // e.g. 48000
	WindowPackets uint64 // close window once watermark passes this many ahead
}

// RTPStream is a per-SSRC processor: parse, dedupe, reorder, then flush.
// It never fabricates missing media; closed-window holes become Gaps.
type RTPStream struct {
	cfg       RTPConfig
	mu        sync.Mutex
	seqUnwrap UnwrapSeq
	tsUnwrap  UnwrapTS
	seen      map[uint64]struct{}
	buf       map[uint64]*rtp.Packet
	nextOut   uint64
	primed    bool
	maxSeq    uint64
	gaps      []Gap
}

// NewRTPStream constructs a stream with validated configuration.
func NewRTPStream(cfg RTPConfig) (*RTPStream, error) {
	if cfg.ClockRate == 0 {
		return nil, errors.New("rtp clock rate must be non-zero")
	}
	if cfg.WindowPackets == 0 {
		cfg.WindowPackets = 64
	}
	return &RTPStream{cfg: cfg, seen: map[uint64]struct{}{}, buf: map[uint64]*rtp.Packet{}}, nil
}

// Push accepts one raw RTP packet. Duplicates are idempotent (nil output).
// Packets inside the window are buffered; the returned slice contains every
// in-order packet released by this push (may be empty).
func (s *RTPStream) Push(pkt *rtp.Packet) ([]*rtp.Packet, error) {
	if pkt.Version != 2 {
		return nil, errors.New("unsupported RTP version")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ext := s.seqUnwrap.Push(pkt.SequenceNumber)
	if _, dup := s.seen[ext]; dup {
		return nil, nil // idempotent duplicate
	}
	if !s.primed {
		// Standard jitter buffer: the first observed seq is the baseline.
		s.primed = true
		s.nextOut = ext
	}
	s.seen[ext] = struct{}{}
	s.buf[ext] = pkt
	if ext > s.maxSeq {
		s.maxSeq = ext
	}

	var out []*rtp.Packet
	for {
		p, ok := s.buf[s.nextOut]
		if !ok {
			break
		}
		out = append(out, p)
		delete(s.buf, s.nextOut)
		s.nextOut++
	}
	s.closeWindows()
	return out, nil
}

func (s *RTPStream) closeWindows() {
	limit := uint64(0)
	if s.maxSeq > s.cfg.WindowPackets {
		limit = s.maxSeq - s.cfg.WindowPackets
	}
	for s.nextOut < limit {
		if _, ok := s.buf[s.nextOut]; ok {
			// A contiguous packet behind the watermark would already have been
			// emitted; stale buffered entries are dropped as the window slides.
			delete(s.buf, s.nextOut)
			s.nextOut++
			continue
		}
		// Hole inside a closed window: record one contiguous gap, never fabricate.
		from := s.nextOut
		to := from
		for to+1 < limit {
			if _, ok := s.buf[to+1]; ok {
				break
			}
			to++
		}
		s.gaps = append(s.gaps, Gap{
			Source:       rtpSource(s.cfg.SSRC),
			FromSequence: from,
			ToSequence:   to,
		})
		s.nextOut = to + 1
	}
}

// Gaps returns a snapshot of gaps detected on closed windows.
func (s *RTPStream) Gaps() []Gap {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Gap, len(s.gaps))
	copy(out, s.gaps)
	return out
}

// TimestampNS converts an unwrapped RTP timestamp to nanoseconds.
func (s *RTPStream) TimestampNS(unwrappedTS uint64) (int64, error) {
	return ScaleToNS(int64(unwrappedTS), s.cfg.ClockRate)
}

// UnwrapTimestamp exposes the 32-bit unwrapper for late packet handling.
func (s *RTPStream) UnwrapTimestamp(ts uint32) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tsUnwrap.Push(ts)
}

func rtpSource(ssrc uint32) string {
	const hex = "0123456789abcdef"
	b := []byte("rtp:0x00000000")
	for i := 0; i < 8; i++ {
		b[len(b)-1-i] = hex[(ssrc>>(4*i))&0xf]
	}
	return string(b)
}
