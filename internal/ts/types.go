// Package ts parses MPEG-2 transport streams on strict 188-byte packet
// boundaries.
//
// The reader never fabricates media.  A packet that fails sync, is
// truncated, or whose continuity counter regresses is reported through the
// shared fault model with the exact input offset; its payload is withheld
// from downstream PSI/PES extraction.
package ts

import (
	"example.com/assetpack/internal/fault"
)

const (
	// PacketSize is the MPEG-2 transport packet size.
	PacketSize = 188
	// SyncByte starts every transport packet.
	SyncByte = 0x47
	// PCRModulus is the 33-bit unwrapped-domain modulus of a PCR base.
	PCRModulus uint64 = 1 << 33
	// PCRClockHz is the PCR base clock (33-bit, 90 kHz).
	PCRClockHz uint64 = 90000
	// PCRWrapThreshold is the minimum backwards delta treated as a wrap
	// candidate: half the modulus.
	PCRWrapThreshold = PCRModulus / 2
	// MaxPCRAdvance bounds a plausible unwrap step: ten seconds of PCR.
	MaxPCRAdvance = 10 * PCRClockHz
	// PCRPIDNone means no PCR is carried yet.
	PCRPIDNone = 0x1FFF
)

// Adaptation field flags.
const (
	AFDiscontinuity = 0x80
	AFRandomAccess  = 0x40
	AFPCR           = 0x10
)

// Packet is one structurally valid 188-byte transport packet.
type Packet struct {
	Offset int64
	PID    uint16
	PUSI   bool
	TEI    bool
	CC     uint8
	AFC    uint8 // adaptation_field_control
	Scram  uint8 // transport_scrambling_control

	Discontinuity bool
	RandomAccess  bool

	HasPCR       bool
	PCRRaw       uint64 // 33-bit base exactly as carried
	PCRExtension uint16 // 9-bit 27 MHz extension
	PCRUnwrapped uint64 // base after modular unwrap
	PCRWraps     uint64 // number of accepted wraps for this PID
	PCRNanos     int64

	Payload []byte // payload bytes, nil when withheld
	Trusted bool   // payload may be used by PSI/PES extraction
}

// Stats summarizes a parse pass.
type Stats struct {
	Bytes          int
	Packets        int
	ValidPackets   int
	SyncErrors     int
	TruncatedBytes int
	CCDuplicates   int
	CCJumps        int
	PCRWraps       int
}

// Stream is the complete parse result feedable to PSI and PES extraction.
type Stream struct {
	Packets []Packet
	Faults  *fault.List
	Stats   Stats
}

// PID returns the packet's PID as an integer.
func (p Packet) IntPID() int { return int(p.PID) }

// PayloadPacket reports whether the packet carries usable payload.
func (p Packet) PayloadPacket() bool { return p.AFC&1 == 1 }

// pidState is the per-PID parser bookkeeping.
type pidState struct {
	haveCC   bool
	lastCC   uint8
	havePCR  bool
	lastRaw  uint64
	unwrap   uint64
	wraps    uint64
	lastTime int64
	mono     int64 // last proven monotonic nanosecond value
}

// parser holds one pass over a byte slice.
type parser struct {
	data   []byte
	stream *Stream
	states map[uint16]*pidState
}

func (p *parser) state(pid uint16) *pidState {
	st := p.states[pid]
	if st == nil {
		st = &pidState{}
		p.states[pid] = st
	}
	return st
}
