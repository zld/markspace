package model

import "fmt"

// Stage identifies the pipeline stage that produced a diagnostic.
type Stage string

const (
	StageIngest   Stage = "ingest"
	StageRTP      Stage = "rtp"
	StageRTCP     Stage = "rtcp"
	StageScan     Stage = "scan"
	StageValidate Stage = "validate"
	StageFragment Stage = "fragment"
	StageMedia    Stage = "media"
	StageMerge    Stage = "merge"
)

// NoOffset marks a diagnostic whose input offset is unknown.
const NoOffset int64 = -1

// NoTrack marks a diagnostic not bound to a specific track.
const NoTrack uint32 = 0

// Diag is a structured diagnostic. Every problem reported by the kernel
// carries the stage, the first confirmable raw input offset, the affected
// byte range [Offset, EndOffset), and whether processing may continue.
type Diag struct {
	Stage       Stage  `json:"stage"`
	Code        string `json:"code"`
	TrackID     uint32 `json:"track_id,omitempty"`
	Offset      int64  `json:"offset"`
	EndOffset   int64  `json:"end_offset"`
	Message     string `json:"message"`
	Recoverable bool   `json:"recoverable"`
}

// Error implements error.
func (d Diag) Error() string {
	return fmt.Sprintf("[%s] %s at offset %d (track %d): %s",
		d.Stage, d.Code, d.Offset, d.TrackID, d.Message)
}

// NewDiag builds a diagnostic with a single-point range.
func NewDiag(stage Stage, code string, offset int64, recoverable bool, msg string) Diag {
	return Diag{
		Stage:       stage,
		Code:        code,
		Offset:      offset,
		EndOffset:   offset,
		Recoverable: recoverable,
		Message:     msg,
	}
}

// Diags is an ordered list of diagnostics with aggregation helpers.
type Diags []Diag

// Add appends a diagnostic.
func (d *Diags) Add(diag Diag) { *d = append(*d, diag) }

// HasFatal reports whether any diagnostic is not recoverable.
func (d Diags) HasFatal() bool {
	for _, diag := range d {
		if !diag.Recoverable {
			return true
		}
	}
	return false
}

// Count returns the number of diagnostics.
func (d Diags) Count() int { return len(d) }

// First returns the first diagnostic by input order, or false.
func (d Diags) First() (Diag, bool) {
	if len(d) == 0 {
		return Diag{}, false
	}
	return d[0], true
}
