package model

// MPEG-TS constants shared across parser, clock and segmenter.
const (
	// PacketSize is the fixed MPEG-TS packet length in bytes.
	PacketSize = 188
	// SyncByte is the synchronization byte that starts every packet.
	SyncByte = 0x47
	// PcrBaseModulus is 2^33, the period of the 33-bit PCR base field.
	PcrBaseModulus int64 = 1 << 33
	// PcrExtModulus is 2^9, the period of the 9-bit PCR extension field.
	PcrExtModulus = 1 << 9
	// ClockHz is the 27 MHz MPEG-TS system clock. PCR values and the
	// monotonic media timeline are both expressed in these units ("ticks").
	ClockHz int64 = 27_000_000
)

// Well-known PIDs.
const (
	PIDPAT uint16 = 0x0000 // program association table
)

// Adaptation field control values (top two bits of the control nibble).
const (
	AFCPayloadOnly     = 0x1
	AFCAdaptationOnly  = 0x2
	AFCAdaptAndPayload = 0x3
)

// TimeBase converts between PCR ticks (27 MHz units) and milliseconds.
// All representations in one processing run share a single TimeBase so that
// segment boundaries are aligned on one media timeline.
type TimeBase struct {
	// SegmentTicks is the target segment duration in 27 MHz ticks.
	SegmentTicks int64
}

// TicksToMs converts 27 MHz ticks to whole milliseconds.
func (b TimeBase) TicksToMs(ticks int64) int64 { return ticks * 1000 / ClockHz }

// MsToTicks converts milliseconds to 27 MHz ticks.
func (b TimeBase) MsToTicks(ms int64) int64 { return ms * ClockHz / 1000 }

// SegmentIndex returns the zero-based segment that a media timestamp belongs
// to, given the configured target segment duration.
func (b TimeBase) SegmentIndex(mediaTicks int64) int {
	if mediaTicks < 0 || b.SegmentTicks <= 0 {
		return 0
	}
	return int(mediaTicks / b.SegmentTicks)
}

// SegmentStart returns the media timestamp of the first tick of segment idx.
func (b TimeBase) SegmentStart(idx int) int64 { return int64(idx) * b.SegmentTicks }

// Packet represents one parsed 188-byte transport packet plus its source
// location. The raw bytes are retained so downstream stages can repackage
// without reconstructing anything.
type Packet struct {
	Offset          int64
	PID             uint16
	Continuity      uint8
	HasPayload      bool
	PayloadStart    bool
	HasAdaptation   bool
	PCR             int64 // raw PCR in 27 MHz units, -1 when absent
	HasPCR          bool
	Raw             [PacketSize]byte
	Payload         []byte // slice into Raw
}
