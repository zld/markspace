package model

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Kind classifies which subsystem produced a fault, which in turn fixes the
// process exit status.
type Kind string

const (
	KindInput  Kind = "input"
	KindIngest Kind = "ingest"
	KindPlan   Kind = "plan"
	KindWorker Kind = "worker"
	KindRender Kind = "render"
	KindVerify Kind = "verify"
)

// Stable machine-readable codes. They never change, so scripts may match them.
const (
	CodeTrackInvalid        = "track_invalid"
	CodeSeqInvalid          = "sequence_invalid"
	CodeSeqDuplicate        = "sequence_duplicate"
	CodeTimeInvalid         = "time_invalid"
	CodeTimeOverflow        = "time_overflow"
	CodeTimeUnderflow       = "time_underflow"
	CodeTimeRegression      = "time_regression"
	CodeSizeBounds          = "size_bounds"
	CodePathInvalid         = "path_invalid"
	CodePathEscape          = "path_escape"
	CodeHashMalformed       = "hash_malformed"
	CodeHashMismatch        = "hash_mismatch"
	CodeColorInvalid        = "color_invalid"
	CodeManifestInvalid     = "manifest_invalid"
	CodePayloadCorrupt      = "payload_corrupt"
	CodePacketDuplicate     = "packet_duplicate"
	CodeFrameMissing        = "frame_missing"
	CodeBudgetConflict      = "budget_conflict"
	CodeQualityFloor        = "quality_floor"
	CodeSizeConflict        = "size_conflict"
	CodeCacheOverflow       = "cache_overflow"
	CodeArtifactMismatch    = "artifact_mismatch"
	CodeConstraintViolation = "constraint_violation"
)

// Fault is the single structured error shared by all subsystems. It carries
// the track, the frame sequence and a media-time offset so a terminal reader
// can pinpoint the offending input without parsing prose.
type Fault struct {
	Kind        Kind    `json:"kind"`
	Code        string  `json:"code"`
	Track       TrackID `json:"track,omitempty"`
	Frame       uint32  `json:"frame,omitempty"`
	OffsetNS    int64   `json:"offsetNs,omitempty"`
	Recoverable bool    `json:"recoverable"`
	Message     string  `json:"message"`
}

// Error implements error.
func (f *Fault) Error() string {
	where := "global"
	if f.Track > 0 {
		where = fmt.Sprintf("track %d frame %d", f.Track, f.Frame)
	}
	recoverability := "hard"
	if f.Recoverable {
		recoverability = "recoverable"
	}
	return fmt.Sprintf("%s/%s [%s, %s] %s", f.Kind, f.Code, where, recoverability, f.Message)
}

// NewFault builds a hard fault.
func NewFault(kind Kind, code string, track TrackID, frame uint32, msg string) *Fault {
	return &Fault{Kind: kind, Code: code, Track: track, Frame: frame, Message: msg}
}

// Recoverable turns f into a recoverable fault, preserving all fields.
func (f *Fault) RecoverableFault() *Fault {
	clone := *f
	clone.Recoverable = true
	return &clone
}

// WithFault fills in location fields that the producer left at zero.
func WithFault(f *Fault, track TrackID, frame uint32) *Fault {
	if f.Track == 0 {
		f.Track = track
	}
	if f.Frame == 0 {
		f.Frame = frame
	}
	return f
}

// AsFault extracts a *Fault from an error chain.
func AsFault(err error) (*Fault, bool) {
	var f *Fault
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}

// faultOf converts any error to a fault, wrapping unknown errors as hard
// input faults so callers always receive a serializable value.
func faultOf(err error) *Fault {
	if f, ok := AsFault(err); ok {
		return f
	}
	return &Fault{Kind: KindInput, Code: CodeManifestInvalid, Message: err.Error()}
}

// Faults is a sortable, JSON-serializable fault list.
type Faults []*Fault

// Error joins every fault; used where an aggregate must satisfy error.
func (fs Faults) Error() string {
	if len(fs) == 0 {
		return "no faults"
	}
	msgs := make([]string, len(fs))
	for i, f := range fs {
		msgs[i] = f.Error()
	}
	return fmt.Sprintf("%d fault(s): %s", len(fs), strings.Join(msgs, "; "))
}

// HasHard reports whether any fault is unrecoverable.
func (fs Faults) HasHard() bool {
	for _, f := range fs {
		if !f.Recoverable {
			return true
		}
	}
	return false
}

// Hard returns only unrecoverable faults.
func (fs Faults) Hard() Faults {
	var out Faults
	for _, f := range fs {
		if !f.Recoverable {
			out = append(out, f)
		}
	}
	return out
}

// Recoverable returns only recoverable faults.
func (fs Faults) Recoverable() Faults {
	var out Faults
	for _, f := range fs {
		if f.Recoverable {
			out = append(out, f)
		}
	}
	return out
}

// Dedupe keeps the first fault per (code, track, frame), preserving order.
func (fs Faults) Dedupe() Faults {
	seen := make(map[string]bool, len(fs))
	out := make(Faults, 0, len(fs))
	for _, f := range fs {
		key := f.Code + "|" + strconv.Itoa(int(f.Track)) + "|" + strconv.FormatUint(uint64(f.Frame), 10)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

// Sort orders faults by kind, track, frame and code for deterministic output.
func (fs Faults) Sort() {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Track != b.Track {
			return a.Track < b.Track
		}
		if a.Frame != b.Frame {
			return a.Frame < b.Frame
		}
		return a.Code < b.Code
	})
}

// ExitCode maps a fault list to the process contract: verification failures
// exit 2, any other hard fault exits 1, and recoverable-only results exit 0.
func ExitCode(fs Faults) int {
	hard := fs.Hard()
	if len(hard) == 0 {
		return 0
	}
	for _, f := range hard {
		if f.Kind == KindVerify {
			return 2
		}
	}
	return 1
}
