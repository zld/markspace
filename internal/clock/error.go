package clock

import "fmt"

// Code is a stable machine-readable failure category.
type Code string

const (
	CodeOK                 Code = "OK"
	CodeUnreadable         Code = "FILE_UNREADABLE"
	CodeTooLarge           Code = "FILE_TOO_LARGE"
	CodeBadHeader          Code = "BAD_MP4_HEADER"
	CodeTruncated          Code = "TRUNCATED_CONTAINER"
	CodeMissingTrack       Code = "MISSING_TRACK_TABLE"
	CodeInvalidTimebase    Code = "INVALID_TIMEBASE"
	CodeInvalidTimestamp   Code = "INVALID_TIMESTAMP"
	CodeOverflow           Code = "TIMESTAMP_OVERFLOW"
	CodeDrift              Code = "DRIFT_EXCEEDED"
	CodeDuplicateTimestamp Code = "DUPLICATE_TIMESTAMP"
	CodeRegression         Code = "TIMESTAMP_REGRESSION"
	CodeAmbiguousWrap      Code = "AMBIGUOUS_WRAP"
	CodeCorruptPacket      Code = "CORRUPT_RTP_PACKET"
	CodeChunkOrder         Code = "CHUNK_ORDER"
	CodeChunkOverlap       Code = "CHUNK_OVERLAP"
	CodeChunkCoverage      Code = "CHUNK_COVERAGE"
	CodeDigestMismatch     Code = "INDEX_DIGEST_MISMATCH"
	CodeCheckpointMismatch Code = "CHECKPOINT_MISMATCH"
	CodeEventOutOfRange    Code = "EVENT_OUT_OF_RANGE"
	CodeBudgetThreshold    Code = "BUDGET_THRESHOLD"
	CodeAnomalyThreshold   Code = "ANOMALY_THRESHOLD"
	CodeInternal           Code = "INTERNAL"
)

// Error is the uniform error type crossing subsystem boundaries. Offset is a
// raw byte offset when known; -1 means no source offset applies.
type Error struct {
	Code       Code   `json:"code"`
	Message    string `json:"message"`
	Path       string `json:"path,omitempty"`
	Offset     int64  `json:"offset"`
	EndOffset  int64  `json:"end_offset,omitempty"`
	StartNS    int64  `json:"start_ns,omitempty"`
	EndNS      int64  `json:"end_ns,omitempty"`
	ChunkIndex int    `json:"chunk_index,omitempty"`
	ExpectedNS int64  `json:"expected_ns,omitempty"`
	ActualNS   int64  `json:"actual_ns,omitempty"`
	Err        error  `json:"-"`
}

// NewError constructs an Error with an unknown (-1) byte offset.
func NewError(code Code, message string, offset int64) *Error {
	return &Error{Code: code, Message: message, Offset: offset}
}

// WithPath attaches a file path.
func (e *Error) WithPath(path string) *Error { e.Path = path; return e }

// WithInterval attaches a source interval in the shared basis.
func (e *Error) WithInterval(startNS, endNS int64) *Error {
	e.StartNS, e.EndNS = startNS, endNS
	return e
}

// WithChunk attaches chunk identity and boundary expectation data.
func (e *Error) WithChunk(index int, expectedNS, actualNS int64) *Error {
	e.ChunkIndex = index
	e.ExpectedNS = expectedNS
	e.ActualNS = actualNS
	return e
}

// Wrap attaches a causal error.
func (e *Error) Wrap(err error) *Error { e.Err = err; return e }

func (e *Error) Error() string {
	where := ""
	if e.Path != "" {
		where = " " + e.Path
	}
	if e.Offset >= 0 {
		where += fmt.Sprintf(" @byte=%d", e.Offset)
	}
	return fmt.Sprintf("%s%s: %s", e.Code, where, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// AsError converts an arbitrary error to *Error, defaulting to CodeInternal.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var ce *Error
	if as, ok := err.(*Error); ok {
		return as
	}
	if errorAs(err, &ce) {
		return ce
	}
	return &Error{Code: CodeInternal, Message: err.Error(), Offset: -1, Err: err}
}

func errorAs(err error, target **Error) bool {
	type wrapper interface{ Unwrap() error }
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		w, ok := err.(wrapper)
		if !ok {
			return false
		}
		err = w.Unwrap()
	}
	return false
}

// Anomaly is a reportable non-fatal or failed check tied to an original
// location and optional media-time interval.
type Anomaly struct {
	Kind        Code   `json:"kind"`
	Severity    string `json:"severity"` // warning, error, failure
	Path        string `json:"path,omitempty"`
	Offset      int64  `json:"byte_offset"`
	EndOffset   int64  `json:"end_byte_offset,omitempty"`
	StartNS     int64  `json:"start_ns,omitempty"`
	EndNS       int64  `json:"end_ns,omitempty"`
	ChunkIndex  int    `json:"chunk_index,omitempty"`
	ExpectedNS  int64  `json:"expected_ns,omitempty"`
	ActualNS    int64  `json:"actual_ns,omitempty"`
	DeviationNS int64  `json:"deviation_ns,omitempty"`
	Message     string `json:"message"`
}

// AnomalyFromError converts a uniform error into an indexable anomaly.
func AnomalyFromError(err error, severity string) Anomaly {
	ce := AsError(err)
	return Anomaly{
		Kind:        ce.Code,
		Severity:    severity,
		Path:        ce.Path,
		Offset:      ce.Offset,
		EndOffset:   ce.EndOffset,
		StartNS:     ce.StartNS,
		EndNS:       ce.EndNS,
		ChunkIndex:  ce.ChunkIndex,
		ExpectedNS:  ce.ExpectedNS,
		ActualNS:    ce.ActualNS,
		DeviationNS: AbsNS(ce.ActualNS - ce.ExpectedNS),
		Message:     ce.Message,
	}
}
