// Package fault defines the structured error model shared by every
// subsystem. Every fault carries its source byte offset and whether recovery
// is possible, so truncated tails, bad CRCs and incomplete sidecars are never
// silently skipped.
package fault

import "fmt"

// Kind enumerates the fault classes the pipeline can report.
type Kind uint8

const (
	KindUnknown Kind = iota
	KindTruncated       // partial TS packet at end of input
	KindCCGap           // continuity counter discontinuity
	KindBadCRC          // PSI section CRC mismatch
	KindCorruptPacket   // packet with invalid sync byte
	KindPCRWrap         // informational: PCR 2^33 wrap expanded on the clock
	KindPSIUpdate       // PAT/PMT version change (informational epoch boundary)
	KindIncompleteIndex // missing or truncated sidecar / unsupported codec
	KindBudget          // memory budget cannot satisfy candidate parameters
	KindInterrupt       // cooperative interruption for checkpoint resume
)

// String returns the stable wire name of a kind.
func (k Kind) String() string {
	switch k {
	case KindTruncated:
		return "truncated"
	case KindCCGap:
		return "cc-gap"
	case KindBadCRC:
		return "bad-crc"
	case KindCorruptPacket:
		return "corrupt-packet"
	case KindPCRWrap:
		return "pcr-wrap"
	case KindPSIUpdate:
		return "psi-update"
	case KindIncompleteIndex:
		return "incomplete-index"
	case KindBudget:
		return "budget"
	case KindInterrupt:
		return "interrupt"
	default:
		return "unknown"
	}
}

// Fault is a single structured problem located at a source offset.
type Fault struct {
	Kind        Kind
	Offset      int64
	Recoverable bool
	Detail      string
	// Bytes carries a size when meaningful, e.g. the missing byte count of a
	// truncated final packet; zero otherwise.
	Bytes int
}

// Error implements the error interface.
func (f Fault) Error() string {
	mode := "unrecoverable"
	if f.Recoverable {
		mode = "recoverable"
	}
	return fmt.Sprintf("%s at offset %d (%s): %s", f.Kind, f.Offset, mode, f.Detail)
}

// Recorder collects faults while a stream is processed. It is intended for
// single-goroutine use; the worker pool funnels faults through its collector.
type Recorder struct {
	faults []Fault
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Record appends one fault.
func (r *Recorder) Record(f Fault) { r.faults = append(r.faults, f) }

// All returns the recorded faults in occurrence order.
func (r *Recorder) All() []Fault {
	out := make([]Fault, len(r.faults))
	copy(out, r.faults)
	return out
}

// Count returns the number of faults of the given kind.
func (r *Recorder) Count(k Kind) int {
	n := 0
	for _, f := range r.faults {
		if f.Kind == k {
			n++
		}
	}
	return n
}

// Recoverable reports whether every recorded fault is recoverable.
func (r *Recorder) Recoverable() bool {
	for _, f := range r.faults {
		if !f.Recoverable {
			return false
		}
	}
	return true
}

// Has reports whether any fault of the given kind was recorded.
func (r *Recorder) Has(k Kind) bool { return r.Count(k) > 0 }
