package timeline

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Code categorises a problem so callers and manifests can group failures
// without parsing message text.
type Code string

const (
	CodeInvalidTime     Code = "invalid_time"
	CodeAmbiguousWrap   Code = "ambiguous_wrap"
	CodeOutOfOrder      Code = "out_of_order"
	CodeDuplicate       Code = "duplicate"
	CodeTruncated       Code = "truncated"
	CodeInvalidLength   Code = "invalid_length"
	CodeUnsupported     Code = "unsupported"
	CodeMissingTrack    Code = "missing_track"
	CodeMissingKeyframe Code = "missing_keyframe"
	CodeNoBoundary      Code = "no_boundary"
	CodeUnrecoverable   Code = "unrecoverable_loss"
	CodeFingerprint     Code = "fingerprint_mismatch"
	CodeCheckpoint      Code = "checkpoint_invalid"
	CodeIO              Code = "io"
	CodeManifest        Code = "manifest_incomplete"
	CodeUsage           Code = "usage"
)

// Severity separates advisory findings from findings which must keep the
// affected data out of a published manifest.
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Issue locates a problem in the input. ByteOffset and Ticks identify the
// physical source position (file offset and, where meaningful, the raw tick in
// the track time base). FrameIndex locates an event inside its source payload.
type Issue struct {
	Severity   Severity `json:"severity"`
	Code       Code     `json:"code"`
	File       string   `json:"file,omitempty"`
	TrackID    uint32   `json:"track_id,omitempty"`
	ByteOffset int64    `json:"byte_offset,omitempty"`
	FrameIndex int      `json:"frame_index,omitempty"`
	Ticks      int64    `json:"ticks,omitempty"`
	TimeScale  uint32   `json:"timescale,omitempty"`
	Message    string   `json:"message"`
}

// String renders the issue with enough source context to reproduce it.
func (i Issue) String() string {
	var b strings.Builder
	sev := i.Severity
	if sev == "" {
		sev = SeverityError
	}
	fmt.Fprintf(&b, "%s[%s]", sev, i.Code)
	if i.File != "" {
		fmt.Fprintf(&b, " file=%s", i.File)
	}
	if i.ByteOffset > 0 {
		fmt.Fprintf(&b, " offset=%d", i.ByteOffset)
	}
	if i.TrackID != 0 {
		fmt.Fprintf(&b, " track=%d", i.TrackID)
	}
	if i.TimeScale > 0 || i.Ticks != 0 {
		fmt.Fprintf(&b, " ticks=%d/%d", i.Ticks, i.TimeScale)
	}
	if i.FrameIndex > 0 {
		fmt.Fprintf(&b, " frame=%d", i.FrameIndex)
	}
	if i.Message != "" {
		fmt.Fprintf(&b, " %s", i.Message)
	}
	return b.String()
}

// LocatedError wraps an Issue so it can flow through error returns while
// keeping the full source location. Unwrap exposes the optional cause.
type LocatedError struct {
	Issue Issue
	Err   error
}

func (e *LocatedError) Error() string {
	if e.Err == nil {
		return e.Issue.String()
	}
	return e.Issue.String() + ": " + e.Err.Error()
}

func (e *LocatedError) Unwrap() error { return e.Err }

// CodeOf returns the stable code carried by err, if any.
func CodeOf(err error) Code {
	var le *LocatedError
	if errors.As(err, &le) {
		return le.Issue.Code
	}
	return ""
}

// IssueOf extracts the located issue carried by err, if any.
func IssueOf(err error) (Issue, bool) {
	var le *LocatedError
	if errors.As(err, &le) {
		return le.Issue, true
	}
	return Issue{}, false
}

func newLocated(code Code, file string, ticks int64, scale uint32, format string, args ...interface{}) *LocatedError {
	return &LocatedError{Issue: Issue{
		Severity:  SeverityError,
		Code:      code,
		File:      file,
		Ticks:     ticks,
		TimeScale: scale,
		Message:   fmt.Sprintf(format, args...),
	}}
}

// codeError is an error carrying only a code, for invalid non-located input
// such as an unwrappable value or bad parameter.
func codeError(code Code, format string, args ...interface{}) error {
	return &LocatedError{Issue: Issue{Severity: SeverityError, Code: code, Message: fmt.Sprintf(format, args...)}}
}

// Issues is an ordered, de-duplicated collection of findings. Ordering is by
// file, offset, track and code so a manifest built from it is stable.
type Issues []Issue

// Add appends an issue unless an identical location and message already exist.
func (is *Issues) Add(i Issue) {
	if i.Severity == "" {
		i.Severity = SeverityError
	}
	for _, existing := range *is {
		if existing.File == i.File && existing.ByteOffset == i.ByteOffset &&
			existing.TrackID == i.TrackID && existing.FrameIndex == i.FrameIndex &&
			existing.Code == i.Code && existing.Message == i.Message {
			return
		}
	}
	*is = append(*is, i)
}

// AddError records err as an issue if it carries a location; otherwise it is
// recorded as an unlocated error with the given fallback code.
func (is *Issues) AddError(code Code, err error) {
	if err == nil {
		return
	}
	var le *LocatedError
	if errors.As(err, &le) {
		is.Add(le.Issue)
		return
	}
	is.Add(Issue{Severity: SeverityError, Code: code, Message: err.Error()})
}

// HasErrors reports whether any recorded issue is fatal to publishing.
func (is Issues) HasErrors() bool {
	for _, i := range is {
		if i.Severity != SeverityWarning {
			return true
		}
	}
	return false
}

// Sorted returns a copy ordered deterministically.
func (is Issues) Sorted() Issues {
	out := append(Issues(nil), is...)
	sort.SliceStable(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.File != y.File {
			return x.File < y.File
		}
		if x.ByteOffset != y.ByteOffset {
			return x.ByteOffset < y.ByteOffset
		}
		if x.TrackID != y.TrackID {
			return x.TrackID < y.TrackID
		}
		if x.FrameIndex != y.FrameIndex {
			return x.FrameIndex < y.FrameIndex
		}
		if x.Code != y.Code {
			return x.Code < y.Code
		}
		return x.Message < y.Message
	})
	return out
}

// Strings renders the sorted issues for terminal output.
func (is Issues) Strings() []string {
	sorted := is.Sorted()
	out := make([]string, len(sorted))
	for i, issue := range sorted {
		out[i] = issue.String()
	}
	return out
}

// Merge appends other, honouring de-duplication.
func (is *Issues) Merge(other Issues) {
	for _, i := range other {
		is.Add(i)
	}
}
