package rtp

import (
	"github.com/pion/rtp"

	"mediasync/internal/clock"
)

// Ingestor wires bounded RTP parsing, sequence reordering and 32-bit
// timestamp unwrap into one per-source pipeline. All emitted packets carry
// extended (unwrapped) timestamps and a monotonic media index.
type Ingestor struct {
	SSRC        uint32
	ClockRate   uint32
	reorder     *ReorderBuffer
	prevExt     int64
	unwrapStart bool
	wraps       int
	firstSeq    uint16
	haveFirst   bool
	emittedIdx  uint64
	firstFail   *clock.Error
}

// NewIngestor constructs an ingest pipeline with the given reorder window.
func NewIngestor(ssrc uint32, rate uint32, window uint16) (*Ingestor, *clock.Error) {
	rb, err := NewReorderBuffer(window)
	if err != nil {
		return nil, err
	}
	if rate == 0 {
		return nil, clock.NewError(clock.ErrInvalidLength, "rtp clock rate must be non-zero")
	}
	return &Ingestor{SSRC: ssrc, ClockRate: rate, reorder: rb}, nil
}

// IngestRaw validates length bounds, parses one RTP datagram and feeds it to
// the reorder buffer. It returns the reorder classification and any parse
// error. The first structural failure is retained for reporting.
func (g *Ingestor) IngestRaw(data []byte) (ReorderStatus, *clock.Error) {
	if len(data) == 0 {
		return StatusQueueOverflow, clock.NewError(clock.ErrInvalidLength, "empty rtp datagram")
	}
	if len(data) > clock.MaxPacketBytes {
		return StatusQueueOverflow, clock.NewError(clock.ErrInvalidLength, "rtp datagram exceeds MaxPacketBytes")
	}
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(data); err != nil {
		ce := clock.NewError(clock.ErrInvalidLength, "rtp unmarshal failed: "+err.Error())
		g.recordFailure(ce)
		return StatusQueueOverflow, ce
	}
	if g.SSRC != 0 && pkt.SSRC != g.SSRC {
		ce := clock.NewError(clock.ErrInvalidLength, "ssrc mismatch on ingested packet")
		g.recordFailure(ce)
		return StatusQueueOverflow, ce
	}
	return g.reorder.Push(pkt), nil
}

// Ordered drains packets that can now be released in sequence order, extending
// each RTP timestamp across 32-bit wraps. The returned frames retain the pion
// packet together with its extended timestamp and emit index.
func (g *Ingestor) Ordered() ([]Frame, *clock.Error) {
	pkts := g.reorder.Drain()
	out := make([]Frame, 0, len(pkts))
	for _, p := range pkts {
		ext, w, err := g.extend(p.Timestamp)
		if err != nil {
			g.recordFailure(err)
			return out, err
		}
		if !g.haveFirst {
			g.haveFirst = true
			g.firstSeq = p.SequenceNumber
		}
		out = append(out, Frame{Packet: p, ExtendedTS: ext, EmitIndex: g.emittedIdx})
		g.emittedIdx++
		g.wraps += w
	}
	return out, nil
}

// Finalize flushes the reorder buffer at end of stream, committing tail holes.
func (g *Ingestor) Finalize() ([]Frame, *clock.Error) {
	pkts := g.reorder.Flush()
	out := make([]Frame, 0, len(pkts))
	for _, p := range pkts {
		ext, w, err := g.extend(p.Timestamp)
		if err != nil {
			g.recordFailure(err)
			return out, err
		}
		out = append(out, Frame{Packet: p, ExtendedTS: ext, EmitIndex: g.emittedIdx})
		g.emittedIdx++
		g.wraps += w
	}
	return out, nil
}

// extend unwraps one timestamp against the running extended value.
func (g *Ingestor) extend(ts uint32) (int64, int, *clock.Error) {
	if !g.unwrapStart {
		g.unwrapStart = true
		g.prevExt = int64(ts)
		return g.prevExt, 0, nil
	}
	next, w, err := clock.UnwrapTS(g.prevExt, ts)
	if err != nil {
		return 0, 0, err
	}
	g.prevExt = next
	return next, w, nil
}

func (g *Ingestor) recordFailure(e *clock.Error) {
	if g.firstFail == nil {
		g.firstFail = e
	}
}

// Frame is one released RTP packet with its extended timestamp and index.
type Frame struct {
	Packet     *rtp.Packet
	ExtendedTS int64
	EmitIndex  uint64
}

// Counters exposes the underlying reorder counters.
func (g *Ingestor) Counters() ReorderCounters { return g.reorder.Counters() }

// Wraps reports the total number of forward RTP timestamp wraps observed.
func (g *Ingestor) Wraps() int { return g.wraps }

// FirstFailure returns the first structural error seen, or nil.
func (g *Ingestor) FirstFailure() *clock.Error { return g.firstFail }

// Emitted reports how many ordered frames have been produced.
func (g *Ingestor) Emitted() uint64 { return g.emittedIdx }
