package media

import (
	"errors"
	"fmt"
	"strings"
)

// Severity classifies how processing should proceed after a fault.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
	SeverityFatal   Severity = "fatal"
)

// Stable fault classes used by terminal output and tests.
const (
	ClassTruncatedMoov  = "truncated_moov"
	ClassBadSTCO        = "stco_out_of_bounds"
	ClassBackwardPTS    = "backward_timestamp"
	ClassOverflow       = "timestamp_overflow"
	ClassContainerOnly  = "container_only_change"
	ClassUnsupportedBox = "unsupported_box"
	ClassHashMismatch   = "hash_mismatch"
	ClassJournal        = "recovery_journal"
	ClassRTPSegmentGap  = "rtp_sequence_gap"
	ClassRTPBadVersion  = "rtp_bad_header"
)

// Recompute scopes tell the indexer which assets are invalidated.
const (
	ScopeNone      = "none"
	ScopeContainer = "container"
	ScopeTrack     = "track"
	ScopeSegment   = "segment"
	ScopeAll       = "all"
)

// Fault is the common structured error model for every subsystem.
type Fault struct {
	Class          string   `json:"class"`
	Code           string   `json:"code,omitempty"`
	Severity       Severity `json:"severity"`
	Path           string   `json:"path,omitempty"`
	Box            string   `json:"box,omitempty"`
	Offset         int64    `json:"offset"`
	TrackID        uint32   `json:"track_id,omitempty"`
	TrackIDs       []uint32 `json:"track_ids,omitempty"`
	Message        string   `json:"message"`
	RecomputeScope string   `json:"recompute_scope,omitempty"`
	Err            error    `json:"-"`
}

func (f *Fault) Error() string {
	var b strings.Builder
	if f.Class != "" {
		b.WriteString(f.Class)
	} else {
		b.WriteString("media_fault")
	}
	if f.Box != "" {
		fmt.Fprintf(&b, " box=%s", f.Box)
	}
	if f.Offset >= 0 {
		fmt.Fprintf(&b, " offset=%d", f.Offset)
	}
	if f.TrackID != 0 {
		fmt.Fprintf(&b, " track=%d", f.TrackID)
	}
	if f.Message != "" {
		b.WriteString(": ")
		b.WriteString(f.Message)
	}
	if f.Err != nil {
		b.WriteString(": ")
		b.WriteString(f.Err.Error())
	}
	return b.String()
}

func (f *Fault) Unwrap() error { return f.Err }

// NewFault constructs a fault with the common location fields.
func NewFault(class string, severity Severity, message string, opts ...FaultOption) *Fault {
	f := &Fault{Class: class, Severity: severity, Message: message, Offset: -1}
	for _, opt := range opts {
		opt(f)
	}
	if f.RecomputeScope == "" {
		f.RecomputeScope = scopeForClass(class)
	}
	return f
}

type FaultOption func(*Fault)

func WithPath(path string) FaultOption {
	return func(f *Fault) { f.Path = path }
}

func WithBox(box string, offset int64) FaultOption {
	return func(f *Fault) { f.Box = box; f.Offset = offset }
}

func WithTrack(trackID uint32) FaultOption {
	return func(f *Fault) { f.TrackID = trackID }
}

func WithTracks(ids ...uint32) FaultOption {
	return func(f *Fault) { f.TrackIDs = append(f.TrackIDs, ids...) }
}

func WithScope(scope string) FaultOption {
	return func(f *Fault) { f.RecomputeScope = scope }
}

func WithCause(err error) FaultOption {
	return func(f *Fault) { f.Err = err }
}

func scopeForClass(class string) string {
	switch class {
	case ClassTruncatedMoov, ClassBadSTCO:
		return ScopeTrack
	case ClassBackwardPTS, ClassOverflow, ClassHashMismatch:
		return ScopeSegment
	case ClassContainerOnly, ClassUnsupportedBox:
		return ScopeContainer
	default:
		return ScopeAll
	}
}

// Report is a collection of non-panicking parse/processing anomalies.
type Report struct {
	Faults []*Fault `json:"faults"`
}

func (r *Report) Add(f *Fault) *Fault {
	if f == nil {
		return nil
	}
	r.Faults = append(r.Faults, f)
	return f
}

func (r *Report) Error(class string, message string, opts ...FaultOption) *Fault {
	return r.Add(NewFault(class, SeverityError, message, opts...))
}

func (r *Report) Warning(class string, message string, opts ...FaultOption) *Fault {
	return r.Add(NewFault(class, SeverityWarning, message, opts...))
}

func (r *Report) HasClass(class string) bool {
	for _, f := range r.Faults {
		if f.Class == class {
			return true
		}
	}
	return false
}

func (r *Report) ByClass(class string) []*Fault {
	var out []*Fault
	for _, f := range r.Faults {
		if f.Class == class {
			out = append(out, f)
		}
	}
	return out
}

func (r *Report) Corrupt() bool {
	for _, f := range r.Faults {
		if f.Severity == SeverityError || f.Severity == SeverityFatal {
			return true
		}
	}
	return false
}

// AsFault extracts a structured Fault from an error chain.
func AsFault(err error) (*Fault, bool) {
	var f *Fault
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}
