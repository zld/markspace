// Package fault defines the shared structured error model used by every
// subsystem.  Faults carry a stable code, the byte offset where the problem was
// observed and the time range (in rational seconds) it covers, so reports stay
// machine readable and damage can be located to the byte.
package fault

import (
	"fmt"

	"mediapipeline/internal/clock"
)

// Code is a stable, language independent fault identifier.
type Code string

const (
	CodeNALIllegalLength Code = "NAL_ILLEGAL_LENGTH"
	CodeNALTruncated     Code = "NAL_TRUNCATED"
	CodeOBUIllegalLength Code = "OBU_ILLEGAL_LENGTH"
	CodeOBUTruncated     Code = "OBU_TRUNCATED"
	CodeIndexOutOfBounds Code = "INDEX_OUT_OF_BOUNDS"
	CodeIndexBadTimeBase Code = "INDEX_TIME_BASE_CONFLICT"
	CodeIndexDuplicate   Code = "INDEX_DUPLICATE_KEY"
	CodeGap              Code = "TRANSPORT_GAP"
	CodeDuplicatePacket  Code = "TRANSPORT_DUPLICATE"
	CodeWindowExceeded   Code = "TRANSPORT_OUT_OF_WINDOW"
	CodeBudgetExceeded   Code = "BUDGET_EXCEEDED"
	CodeLimitExceeded    Code = "LIMIT_EXCEEDED"
	CodeAssetMissing     Code = "ASSET_MISSING"
	CodeIndexConflict    Code = "ASSET_INDEX_CONFLICT"
	CodeByteConflict     Code = "ASSET_BYTE_CONFLICT"
	CodeChecksumMismatch Code = "CHECKSUM_MISMATCH"
	CodeBoundaryInvalid  Code = "BOUNDARY_INVALID"
	CodeNonMonotonic     Code = "TIME_NON_MONOTONIC"
	CodeCheckpointBad    Code = "CHECKPOINT_CORRUPT"
)

// Fault is one structured problem record.
type Fault struct {
	Code     Code           `json:"code"`
	Message  string         `json:"message"`
	Offset   int64          `json:"offset"`    // byte offset in the input, -1 when N/A
	TimeFrom clock.Rational `json:"time_from"` // inclusive start, seconds
	TimeTo   clock.Rational `json:"time_to"`   // inclusive end, seconds
}

// New builds a fault; offset -1 means "no byte position".
func New(code Code, msg string, offset int64, from, to clock.Rational) Fault {
	return Fault{Code: code, Message: msg, Offset: offset, TimeFrom: from, TimeTo: to}
}

// Error implements the error interface for a single fault.
func (f Fault) Error() string {
	if f.Offset >= 0 {
		return fmt.Sprintf("%s at offset %d [%s..%s]: %s",
			f.Code, f.Offset, f.TimeFrom, f.TimeTo, f.Message)
	}
	return fmt.Sprintf("%s [%s..%s]: %s", f.Code, f.TimeFrom, f.TimeTo, f.Message)
}

// Report is an ordered collection of faults.  Subsystems keep collecting after
// a fault so a single run can describe every recoverable problem.
type Report struct {
	faults []Fault
}

// Add records a fault and returns it.
func (r *Report) Add(f Fault) Fault {
	r.faults = append(r.faults, f)
	return f
}

// Addf is a convenience constructor mirroring New.
func (r *Report) Addf(code Code, offset int64, from, to clock.Rational, format string, args ...interface{}) Fault {
	return r.Add(New(code, fmt.Sprintf(format, args...), offset, from, to))
}

// All returns the recorded faults in insertion order.
func (r *Report) All() []Fault {
	out := make([]Fault, len(r.faults))
	copy(out, r.faults)
	return out
}

// Has reports whether any fault of the given codes exists.
func (r *Report) Has(codes ...Code) bool {
	want := make(map[Code]struct{}, len(codes))
	for _, c := range codes {
		want[c] = struct{}{}
	}
	for _, f := range r.faults {
		if _, ok := want[f.Code]; ok {
			return true
		}
	}
	return false
}

// Count returns the number of recorded faults.
func (r *Report) Count() int { return len(r.faults) }

// CountCode returns how many faults carry the given code.
func (r *Report) CountCode(code Code) int {
	n := 0
	for _, f := range r.faults {
		if f.Code == code {
			n++
		}
	}
	return n
}

// Merge appends another report's faults.
func (r *Report) Merge(other *Report) {
	if other == nil {
		return
	}
	r.faults = append(r.faults, other.faults...)
}
