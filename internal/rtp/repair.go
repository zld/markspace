package rtp

import "fmt"

// EventKind enumerates everything the jitter stage can emit.
type EventKind uint8

const (
	EventPacket EventKind = iota // in-order real packet
	EventGap                     // unrecovered missing sequence: explicit hole, no fabricated media
	EventRecovered               // missing packet rebuilt from repair information
	EventLate                    // packet arrived outside the reorder window and was discarded
	EventDuplicate               // repeated sequence number, never enters the output twice
)

// String renders the stable event label used in fault reports.
func (k EventKind) String() string {
	switch k {
	case EventPacket:
		return "packet"
	case EventGap:
		return "gap"
	case EventRecovered:
		return "recovered"
	case EventLate:
		return "late"
	case EventDuplicate:
		return "duplicate"
	default:
		return "unknown"
	}
}

// Event is one output record. Every loss/late/repair event carries both the
// sequence number and its position on the shared nanosecond time base.
type Event struct {
	Kind     EventKind
	Packet   Packet
	ExtSeq   uint64
	Nanos    int64
	SSRC     uint32
	PT       uint8
	Detail   string
}

// Recoverer supplies bounded repair. Implementations must be deterministic and
// must return ok=false (never a fabricated payload) when repair information
// is insufficient. present/payloadOf give access to currently buffered
// packets of the same stream without exposing buffer internals.
type Recoverer interface {
	Recover(extSeq uint64, present func(uint64) bool, payloadOf func(uint64) []byte) (payload []byte, rtpTime uint32, ok bool)
	Name() string
}

// ParityGroup is an XOR repair group: packets' payloads XORed together produce
// parity, so any single missing member can be reconstructed.
type ParityGroup struct {
	Members []uint64 // extended sequence numbers
	RTPTime uint32
	Parity  []byte
}

// XORRepair holds repair groups keyed by the missing sequence they could
// restore. It models generic-parity / FEC recovery without network I/O.
type XORRepair struct {
	groups map[uint64]ParityGroup
}

// NewXORRepair builds an empty repair store.
func NewXORRepair() *XORRepair { return &XORRepair{groups: map[uint64]ParityGroup{}} }

// AddGroup registers one parity group. All member payloads must share length.
func (x *XORRepair) AddGroup(g ParityGroup) error {
	for _, m := range g.Members {
		if _, exists := x.groups[m]; exists {
			continue // a sequence may be covered by several groups; keep first
		}
		x.groups[m] = g
	}
	return nil
}

// Name identifies the repair strategy in reports.
func (x *XORRepair) Name() string { return "xor-parity" }

// Recover rebuilds one packet only when exactly one member of its covering
// group is absent. Two or more simultaneous losses exceed the repair bound.
func (x *XORRepair) Recover(extSeq uint64, present func(uint64) bool, payloadOf func(uint64) []byte) ([]byte, uint32, bool) {
	g, ok := x.groups[extSeq]
	if !ok {
		return nil, 0, false
	}
	missing := 0
	for _, m := range g.Members {
		if !present(m) {
			missing++
		}
	}
	if missing != 1 {
		return nil, 0, false
	}
	out := append([]byte(nil), g.Parity...)
	for _, m := range g.Members {
		if m == extSeq || !present(m) {
			continue
		}
		pl := payloadOf(m)
		if len(pl) != len(out) {
			return nil, 0, false // unequal lengths make XOR recovery ambiguous
		}
		for i := range out {
			out[i] ^= pl[i]
		}
	}
	return out, g.RTPTime, true
}

// gapNanos estimates the shared-base position of a missing packet by linear
// interpolation of RTP timestamps between the nearest present neighbours.
func gapNanos(ext uint64, prev, next *Packet, tickNS int64) (int64, error) {
	switch {
	case prev != nil && next != nil:
		span := next.ExtSeq - prev.ExtSeq
		if span == 0 {
			return 0, fmt.Errorf("zero span around ext seq %d", ext)
		}
		steps := ext - prev.ExtSeq
		return prev.Nanos + (next.Nanos-prev.Nanos)*int64(steps)/int64(span), nil
	case prev != nil:
		return prev.Nanos + tickNS*int64(ext-prev.ExtSeq), nil
	case next != nil:
		return next.Nanos - tickNS*int64(next.ExtSeq-ext), nil
	default:
		return 0, fmt.Errorf("no neighbours to position ext seq %d", ext)
	}
}
