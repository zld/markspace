// Package report defines the single finding/record model used by every
// subsystem and the terminal formatter.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Stable error codes. They are part of the command-line interface.
const (
	OK = "OK"

	EBMLInvalidVINT       = "EBML_INVALID_VINT"
	EBMLIllegalLength     = "EBML_ILLEGAL_LENGTH"
	EBMLTruncated         = "EBML_TRUNCATED_ELEMENT"
	EBMLUnknownBoundary   = "EBML_UNKNOWN_BOUNDARY"
	EBMLIncompleteBlock   = "EBML_INCOMPLETE_BLOCK"
	EBMLLimit             = "EBML_LIMIT_EXCEEDED"
	EBMLUnknownElement    = "EBML_UNKNOWN_ELEMENT"
	TimelineBadBase       = "TIMELINE_BAD_TIMEBASE"
	TimelineOverflow      = "TIMELINE_OVERFLOW"
	TimelineWrapAmbiguous = "TIMESTAMP_WRAP_AMBIGUOUS"

	TrackLayoutConflict     = "TRACK_LAYOUT_CONFLICT"
	TrackSampleRateConflict = "TRACK_SAMPLE_RATE_CONFLICT"
	TrackColorConflict      = "TRACK_COLOR_CONFLICT"
	TrackDependency         = "TRACK_DEPENDENCY_INVALID"

	FragmentTimeOrder        = "FRAGMENT_TIME_ORDER"
	FragmentCoverage         = "FRAGMENT_CROSS_TRACK_COVERAGE"
	FragmentLimit            = "FRAGMENT_LIMIT_EXCEEDED"
	RTPGapRecovered          = "RTP_GAP_RECOVERED"
	RTPSequenceDiscontinuity = "RTP_SEQUENCE_DISCONTINUITY"
	RTPTimelineOutOfBounds   = "RTP_TIMELINE_OUT_OF_BOUNDS"
	RTPMalformedPacket       = "RTP_MALFORMED_PACKET"
)

// Severity values keep a recovered gap distinct from a fatal validation error.
const (
	SeverityInfo      = "info"
	SeverityRecovered = "recovered"
	SeverityError     = "error"
)

// Record is the fixed output schema. The eight required fields are always
// present, even when a subsystem adds more specific context.
type Record struct {
	Type          string `json:"type"`
	Sample        string `json:"sample"`
	Track         int    `json:"track"`
	InputOffset   int    `json:"input_offset"`
	TimeDeltaNS   int64  `json:"time_delta_ns"`
	ByteDelta     int64  `json:"byte_delta"`
	FailureOffset int    `json:"failure_offset"`
	ErrorCode     string `json:"error_code"`
	SHA256        string `json:"sha256"`

	Severity     string `json:"severity,omitempty"`
	Category     string `json:"error_category,omitempty"`
	ElementID    string `json:"element_id,omitempty"`
	Fragment     string `json:"fragment,omitempty"`
	Sequence     int    `json:"rtp_sequence,omitempty"`
	MissingCount int    `json:"missing_count,omitempty"`
	TimeStartNS  int64  `json:"time_start_ns,omitempty"`
	TimeEndNS    int64  `json:"time_end_ns,omitempty"`
	Message      string `json:"message,omitempty"`
}

// Error is a Go error carrying a structured report record.
type Error struct {
	Record Record
}

func (e *Error) Error() string {
	code := e.Record.ErrorCode
	if code == "" {
		code = "ERROR"
	}
	if e.Record.ElementID != "" {
		return fmt.Sprintf("%s at input_offset=%d failure_offset=%d element=%s: %s",
			code, e.Record.InputOffset, e.Record.FailureOffset, e.Record.ElementID, e.Record.Message)
	}
	return fmt.Sprintf("%s at input_offset=%d failure_offset=%d: %s",
		code, e.Record.InputOffset, e.Record.FailureOffset, e.Record.Message)
}

// AsError converts an error created by this package, including wrapped errors.
func AsError(err error) (*Error, bool) {
	for err != nil {
		if e, ok := err.(*Error); ok {
			return e, true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return nil, false
		}
		err = u.Unwrap()
	}
	return nil, false
}

// NewError builds a structured error with the mandatory zero-valued fields.
func NewError(code, category, elementID string, inputOffset, failureOffset int, format string, args ...any) *Error {
	return &Error{Record: Record{
		Type:          "issue",
		ErrorCode:     code,
		Category:      category,
		ElementID:     elementID,
		InputOffset:   inputOffset,
		FailureOffset: failureOffset,
		Severity:      SeverityError,
		Message:       fmt.Sprintf(format, args...),
	}}
}

// Summary is the deterministic final record.
type Summary struct {
	Type             string `json:"type"`
	Sample           string `json:"sample"`
	Case             string `json:"case"`
	Seed             int64  `json:"seed"`
	SHA256           string `json:"sha256"`
	Records          int    `json:"records"`
	Blocks           int    `json:"blocks"`
	Fragments        int    `json:"fragments"`
	Issues           int    `json:"issues"`
	Errors           int    `json:"errors"`
	RecoveredMissing int    `json:"recovered_missing_packets"`
	ExitCode         int    `json:"exit_code"`
}

// Report is the complete stable JSON document.
type Report struct {
	Summary Summary  `json:"summary"`
	Records []Record `json:"records"`
}

// Finalize fills sample/digest on all records and computes counts. It also
// sorts records by a stable key while preserving raw block order through the
// sequence/offset fields already present on each record.
func (r *Report) Finalize(sampleName, caseName, digest string, seed int64) {
	if r.Records == nil {
		r.Records = []Record{}
	}
	for i := range r.Records {
		r.Records[i].Sample = sampleName
		r.Records[i].SHA256 = digest
		if r.Records[i].Type == "" {
			r.Records[i].Type = "record"
		}
	}
	sort.SliceStable(r.Records, func(i, j int) bool {
		a, b := r.Records[i], r.Records[j]
		if a.InputOffset != b.InputOffset {
			return a.InputOffset < b.InputOffset
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Track != b.Track {
			return a.Track < b.Track
		}
		if a.ErrorCode != b.ErrorCode {
			return a.ErrorCode < b.ErrorCode
		}
		return a.FailureOffset < b.FailureOffset
	})

	s := Summary{Type: "summary", Sample: sampleName, Case: caseName, Seed: seed, SHA256: digest, Records: len(r.Records)}
	for _, rec := range r.Records {
		switch rec.Type {
		case "block":
			s.Blocks++
		case "fragment":
			s.Fragments++
		case "issue":
			s.Issues++
			if rec.Severity == SeverityError {
				s.Errors++
			}
			if rec.Severity == SeverityRecovered {
				s.RecoveredMissing += rec.MissingCount
			}
		case "recovery":
			s.RecoveredMissing += rec.MissingCount
		}
	}
	if s.Errors > 0 {
		s.ExitCode = 1
	}
	r.Summary = s
}

// WriteJSON emits one indented deterministic JSON report.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteTerminal writes the JSON report followed by a compact human summary.
func (r *Report) WriteTerminal(w io.Writer) error {
	var buf strings.Builder
	if err := r.WriteJSON(&buf); err != nil {
		return err
	}
	if _, err := io.WriteString(w, buf.String()); err != nil {
		return err
	}
	fmt.Fprintf(w, "sample=%s sha256=%s blocks=%d fragments=%d issues=%d errors=%d recovered_missing=%d exit_code=%d\n",
		r.Summary.Sample, r.Summary.SHA256, r.Summary.Blocks, r.Summary.Fragments,
		r.Summary.Issues, r.Summary.Errors, r.Summary.RecoveredMissing, r.Summary.ExitCode)
	return nil
}
