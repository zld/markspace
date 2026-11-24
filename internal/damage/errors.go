// Package damage defines the shared error model and the logic that locates
// damaged regions of a media asset on the unified nanosecond timeline.
package damage

import "fmt"

// Code is the stable, machine-readable category of a media defect. Every
// defect reported by any subsystem uses one of these codes so that pipeline
// output and tests can match on cause rather than wording.
type Code string

const (
	// CodeUnparseable means the container could not be parsed at all.
	CodeUnparseable Code = "unparseable"
	// CodeTruncatedBox means a box header/payload is shorter than its
	// declared size (index truncation, cut file).
	CodeTruncatedBox Code = "truncated_box"
	// CodeMissingIndex means a required sample table box is absent.
	CodeMissingIndex Code = "missing_index"
	// CodeIndexUndecidable means counts across sample tables disagree, so
	// the true sample count cannot be determined.
	CodeIndexUndecidable Code = "index_undecidable"
	// CodeMissingSample means a sample referenced by the table is absent.
	CodeMissingSample Code = "missing_sample"
	// CodeRTPLost means one or more RTP sequence numbers were never seen.
	CodeRTPLost Code = "rtp_lost"
	// CodeRTPReordered means packets arrived out of sequence order.
	CodeRTPReordered Code = "rtp_reordered"
	// CodeRTPDuplicate means the same sequence number arrived twice.
	CodeRTPDuplicate Code = "rtp_duplicate"
	// CodeUnrecoveredLoss means lost packets could not be reconstructed.
	CodeUnrecoveredLoss Code = "unrecovered_loss"
	// CodeDependencyMissing means a track's dependency edge has no source.
	CodeDependencyMissing Code = "dependency_missing"
	// CodeDependencyCycle means the track graph contains a cycle.
	CodeDependencyCycle Code = "dependency_cycle"
)

// Recoverable classifies whether a defect can be repaired automatically.
type Recoverable string

const (
	// RecoverableYes: the recovery module repaired the data.
	RecoverableYes Recoverable = "yes"
	// RecoverableNo: no automatic repair is possible; the region stays
	// explicit in every downstream statistic.
	RecoverableNo Recoverable = "no"
	// RecoverableUnknown: the defect has not yet passed the recovery stage.
	RecoverableUnknown Recoverable = "unknown"
)

// Range is a half-open interval on the unified nanosecond timeline.
// Undecidable ranges carry Deterministic=false: their endpoints are index
// inferences, not decoded boundaries.
type Range struct {
	StartNanos    int64       `json:"start_nanos"`
	EndNanos      int64       `json:"end_nanos"`
	Deterministic bool        `json:"deterministic"`
	Recoverable   Recoverable `json:"recoverable"`
}

// LengthNanos returns the interval duration.
func (r Range) LengthNanos() int64 { return r.EndNanos - r.StartNanos }

// Overlaps reports whether two ranges intersect.
func (r Range) Overlaps(o Range) bool {
	return r.StartNanos < o.EndNanos && o.StartNanos < r.EndNanos
}

// Error is the single structured defect record shared by all subsystems.
// It always carries a code, the affected track (empty for container-level
// faults), a timeline range (zero-length when no time mapping exists yet),
// and a human-readable detail message.
type Error struct {
	Code        Code        `json:"code"`
	TrackID     uint32      `json:"track_id"`
	Range       Range       `json:"range"`
	Detail      string      `json:"detail"`
	Recoverable Recoverable `json:"recoverable"`
}

// Error implements the error interface.
func (e Error) Error() string {
	return fmt.Sprintf("damage[%s] track=%d range=[%d,%d)ns %s",
		e.Code, e.TrackID, e.Range.StartNanos, e.Range.EndNanos, e.Detail)
}

// SetRecoverable stamps the recoverability of the defect and of its range.
func (e *Error) SetRecoverable(r Recoverable) *Error {
	e.Recoverable = r
	e.Range.Recoverable = r
	return e
}
