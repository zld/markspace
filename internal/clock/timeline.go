// Package clock defines the project-wide 90 kHz time base, PCR unwrap
// semantics and a single structured error model shared by every subsystem.
package clock

import (
	"fmt"

	"github.com/pion/rtp"
)

// Rate is the MPEG-2 systems clock reference rate in ticks per second used
// everywhere in this project (90 kHz, 27 MHz PCR divided by 300).
const Rate int64 = 90000

// Mod33 is the modulus of the 33-bit PCR base field.
const Mod33 int64 = 1 << 33

// Mod32 is the modulus of a 32-bit RTP timestamp.
const Mod32 int64 = 1 << 32

// Half33 is half of the PCR modulus, the wrap decision threshold.
const Half33 = Mod33 / 2

// Half32 is half of the RTP timestamp modulus.
const Half32 = Mod32 / 2

// JumpTicks is the forward jump above which a discontinuity is reported even
// without a wrap: one second of 90 kHz ticks.
const JumpTicks = Rate

// Kind enumerates the structured error types understood by the pipeline.
type Kind string

const (
	KindSync          Kind = "sync_error"
	KindTruncated     Kind = "truncated"
	KindContinuity    Kind = "continuity"
	KindPSIVersion    Kind = "psi_version"
	KindCRC           Kind = "crc"
	KindPCRWrap       Kind = "pcr_wrap"
	KindDiscontinuity Kind = "discontinuity"
)

// RangeError is the only error model in the project: every anomaly carries a
// kind, a human message and a half-open time range [Start,End) in monotonic
// 90 kHz ticks. Slicing never produces a boundary inside an open range.
type RangeError struct {
	Kind  Kind   `json:"kind"`
	Start int64  `json:"start_ticks"`
	End   int64  `json:"end_ticks"`
	Msg   string `json:"msg"`
}

// Error implements the error interface.
func (e RangeError) Error() string {
	return fmt.Sprintf("%s: %s [ticks %d..%d)", e.Kind, e.Msg, e.Start, e.End)
}

// Contains reports whether t falls inside the half-open range.
func (e RangeError) Contains(t int64) bool { return t >= e.Start && t < e.End }

// Unwrap33 expands a raw 33-bit PCR base value onto a monotonic axis.
// A backward delta larger than half the modulus is interpreted as a wrap
// (modulus added back); smaller backward deltas and forward jumps above one
// second are discontinuities. The boolean reports whether a wrap occurred.
func Unwrap33(prev, raw int64) (mono int64, wrapped bool, discontinuity bool) {
	delta := raw - (prev % Mod33)
	switch {
	case delta < -Half33:
		delta += Mod33 // raw value wrapped forward past 2^33
		wrapped = true
	case delta < 0:
		discontinuity = true
	case delta > JumpTicks:
		discontinuity = true
	}
	return prev + delta, wrapped, discontinuity
}

// Unwrap32 expands a raw 32-bit RTP timestamp onto a monotonic axis using the
// standard half-modulus wrap rule shared with the 33-bit PCR logic.
func Unwrap32(prev int64, raw uint32) (mono int64, wrapped bool) {
	delta := int64(raw) - (prev % Mod32)
	if delta < -Half32 {
		delta += Mod32
		wrapped = true
	}
	return prev + delta, wrapped
}

// UnwrapRTP extends the 32-bit RTP timestamp of pkt onto the monotonic axis
// anchored at prev (a previous extended tick). pion/rtp v1.10 carries no
// timestamp extender of its own, so the library's rtp.Packet type supplies the
// raw timestamp and this project's half-modulus rule supplies the extension.
func UnwrapRTP(prev int64, pkt *rtp.Packet) (mono int64, wrapped bool) {
	return Unwrap32(prev, pkt.Timestamp)
}

// SeqRollover drives a pion/rtp Sequencer for n increments and returns the
// observed 16-bit sequence numbers together with the library's rollover
// count, so tests can cross-check wrap detection against an external source.
func SeqRollover(seq rtp.Sequencer, n int) (values []uint16, rollovers uint64) {
	for i := 0; i < n; i++ {
		values = append(values, seq.NextSequenceNumber())
	}
	return values, seq.RollOverCount()
}

// Timeline accumulates PCR observations and yields a monotonic time axis.
// All observed ranges are retained; callers never need to repair time values.
type Timeline struct {
	lastRaw int64
	last    int64
	started bool
	// Errs holds every wrap/discontinuity range observed on this timeline.
	Errs []RangeError
}

// NewTimeline returns an empty timeline.
func NewTimeline() *Timeline { return &Timeline{} }

// Feed ingests one raw 33-bit PCR base (27 MHz PCR already divided by 300),
// returns its monotonic tick and appends a structured range when the sample
// represents a wrap or an unhandled discontinuity.
func (t *Timeline) Feed(raw int64) int64 {
	raw &= Mod33 - 1
	if !t.started {
		t.started = true
		t.lastRaw, t.last = raw, raw
		return raw
	}
	mono, wrapped, disc := Unwrap33(t.last, raw)
	switch {
	case wrapped:
		t.Errs = append(t.Errs, RangeError{
			Kind: KindPCRWrap, Start: t.last, End: mono,
			Msg: "PCR base wrapped through 2^33",
		})
	case disc:
		t.Errs = append(t.Errs, RangeError{
			Kind: KindDiscontinuity, Start: t.last, End: mono,
			Msg: "PCR discontinuity without wrap",
		})
	}
	t.lastRaw, t.last = raw, mono
	return mono
}

// Mono reports the most recent monotonic tick.
func (t *Timeline) Mono() int64 { return t.last }

// HasErrorAt reports whether any recorded error range covers tick x.
func (t *Timeline) HasErrorAt(x int64) bool {
	for _, e := range t.Errs {
		if e.Contains(x) {
			return true
		}
	}
	return false
}

// Ms converts 90 kHz ticks to milliseconds, rounded to the nearest ms.
func Ms(ticks int64) int64 {
	return (ticks*1000 + Rate/2) / Rate
}

// FromMs converts milliseconds to 90 kHz ticks.
func FromMs(ms int64) int64 { return ms * Rate / 1000 }
