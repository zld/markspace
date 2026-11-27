// Package fault defines the single error model used by the RTP/RTCP
// receiver, stream inspector and damage locator.
//
// A Fault records where a problem starts (byte offset inside a packet or
// payload), the media-time range it affects, and whether recovery was
// possible. Components never fabricate media data for a missing range: they
// either recover it from later packets or mark the fault as unrecovered.
package fault

import (
	"errors"
	"fmt"

	"media-check/internal/clock"
)

// Code classifies a fault independently of its originating component.
type Code string

const (
	CodeTruncated       Code = "truncated_packet"
	CodeCorruptPayload  Code = "corrupt_payload"
	CodeDuplicate       Code = "duplicate_packet"
	CodeLost            Code = "lost_packets"
	CodeSequenceWrap    Code = "sequence_wrap_ambiguous"
	CodeTimebaseWrap    Code = "timebase_wrap_ambiguous"
	CodeUnrecoverable   Code = "unrecoverable_unit"
	CodeLedgerInvariant Code = "ledger_invariant_violation"
)

// Severity ranks faults for reporting and exit-code selection.
type Severity string

const (
	SeverityRecovered Severity = "recovered"
	SeverityWarning   Severity = "warning"
	SeverityFatal     Severity = "fatal"
)

// Fault is one concrete problem observed while processing the stream.
type Fault struct {
	Code     Code     `json:"code"`
	Severity Severity `json:"severity"`
	TrackID  uint32   `json:"track_id"`
	// FirstErrOffset is the byte offset (inside the offending packet/payload)
	// at which the first parsing error was detected.
	FirstErrOffset int64 `json:"first_err_offset"`
	// SeqStart/SeqEnd are the extended RTP sequence-number range affected.
	// Both are zero when the fault is not tied to RTP packets.
	SeqStart uint64 `json:"seq_start,omitempty"`
	SeqEnd   uint64 `json:"seq_end,omitempty"`
	// TimeStart/TimeEnd are the media-time range affected, on the unified
	// rational time base. Zero instants are valid (stream origin).
	TimeStart clock.Rational `json:"time_start"`
	TimeEnd   clock.Rational `json:"time_end"`
	Message   string         `json:"message"`
	// Recovered distinguishes "observed and rebuilt" from permanent damage.
	Recovered bool `json:"recovered"`
}

// Model is the shared collector. Every pipeline component appends to the
// same instance so analysis and localization agree on what was damaged.
type Model struct {
	faults []Fault
}

// NewModel returns an empty fault model.
func NewModel() *Model { return &Model{} }

// Record appends a fault, keeping the slice ordered by track then time.
func (m *Model) Record(f Fault) *Fault {
	if f.Message == "" {
		f.Message = string(f.Code)
	}
	m.faults = append(m.faults, f)
	return &m.faults[len(m.faults)-1]
}

// All returns a copy of every recorded fault.
func (m *Model) All() []Fault {
	out := make([]Fault, len(m.faults))
	copy(out, m.faults)
	return out
}

// ByTrack returns the faults belonging to one track.
func (m *Model) ByTrack(trackID uint32) []Fault {
	var out []Fault
	for _, f := range m.faults {
		if f.TrackID == trackID {
			out = append(out, f)
		}
	}
	return out
}

// HasFatal reports whether an unrecoverable fault was recorded.
func (m *Model) HasFatal() bool {
	for _, f := range m.faults {
		if f.Severity == SeverityFatal {
			return true
		}
	}
	return false
}

// TimebaseFaults reports faults that make the rational time base ambiguous;
// the CLI maps these to exit code 2.
func (m *Model) TimebaseFaults() []Fault {
	var out []Fault
	for _, f := range m.faults {
		if f.Code == CodeTimebaseWrap || f.Code == CodeSequenceWrap {
			out = append(out, f)
		}
	}
	return out
}

// Unrecovered returns faults that permanently damage media output.
func (m *Model) Unrecovered() []Fault {
	var out []Fault
	for _, f := range m.faults {
		if !f.Recovered && f.Severity != SeverityWarning {
			out = append(out, f)
		}
	}
	return out
}

// CountByCode summarizes faults for deterministic report output.
func (m *Model) CountByCode() map[Code]int {
	out := make(map[Code]int)
	for _, f := range m.faults {
		out[f.Code]++
	}
	return out
}

// ErrTimebase is the sentinel error for ambiguous time-base decisions.
var ErrTimebase = errors.New("fault: ambiguous time-base wrap")

// ErrRecover is the sentinel error for failed recovery / ledger invariants.
var ErrRecover = errors.New("fault: recovery or ledger invariant failure")

// Wrap annotates a sentinel with context while preserving errors.Is.
func Wrap(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
}
