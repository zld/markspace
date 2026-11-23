// Package fault defines the error model shared by every media pipeline stage.
//
// Every error carries the stage that detected it, the kind of fault, the raw
// input byte offset (when known) and the affected track. The pipeline turns
// these records into the provenance manifest so an operator can locate the
// original bytes that caused a failure.
package fault

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Stage identifies the pipeline stage that produced a fault.
type Stage string

const (
	StageParse    Stage = "parse"
	StageChecksum Stage = "checksum"
	StageJitter   Stage = "jitter"
	StageRecovery Stage = "recovery"
	StageSync     Stage = "sync"
	StageMux      Stage = "mux"
	StageIndex    Stage = "index"
	StagePipeline Stage = "pipeline"
)

// Kind classifies a fault. Kinds are stable strings because they are written
// verbatim into provenance manifests.
type Kind string

const (
	KindTruncated  Kind = "truncated_record"
	KindChecksum   Kind = "checksum_mismatch"
	KindLength     Kind = "length_out_of_range"
	KindVersion    Kind = "unsupported_version"
	KindTrack      Kind = "invalid_track"
	KindTimestamp  Kind = "invalid_timestamp"
	KindMetadata   Kind = "invalid_metadata"
	KindDuplicate  Kind = "duplicate_packet"
	KindLate       Kind = "late_packet"
	KindWindow     Kind = "window_overflow"
	KindLoss       Kind = "loss_gap"
	KindIncomplete Kind = "fragment_incomplete"
	KindDesync     Kind = "out_of_sync"
	KindConfig     Kind = "invalid_config"
	KindIO         Kind = "io_error"
	KindLimit      Kind = "limit_exceeded"
	KindIndex      Kind = "index_miss"
	KindRTPSyntax  Kind = "rtp_syntax"
)

// Error is the structured error used across the processing kernel.
type Error struct {
	Stage       Stage
	Kind        Kind
	Track       uint8
	Offset      int64 // raw input byte offset, -1 when not applicable
	Record      int   // zero-based record index, -1 when not applicable
	Detail      string
	Recoverable bool
	Err         error
}

// New creates a fault with unset offset/record markers.
func New(stage Stage, kind Kind, format string, args ...any) *Error {
	return &Error{
		Stage:  stage,
		Kind:   kind,
		Offset: -1,
		Record: -1,
		Detail: fmt.Sprintf(format, args...),
	}
}

// Wrap attaches an underlying error and marks the fault recoverable.
func Wrap(stage Stage, kind Kind, err error, format string, args ...any) *Error {
	e := New(stage, kind, format, args...)
	e.Err = err
	e.Recoverable = true
	return e
}

// At records the raw input offset.
func (e *Error) At(offset int64, record int) *Error {
	e.Offset = offset
	e.Record = record
	return e
}

// WithTrack records the affected track.
func (e *Error) WithTrack(track uint8) *Error {
	e.Track = track
	return e
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "stage=%s kind=%s", e.Stage, e.Kind)
	if e.Track != 0 {
		fmt.Fprintf(&b, " track=%d", e.Track)
	}
	if e.Offset >= 0 {
		fmt.Fprintf(&b, " offset=%d", e.Offset)
	}
	if e.Record >= 0 {
		fmt.Fprintf(&b, " record=%d", e.Record)
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, " detail=%q", e.Detail)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, " cause=%q", e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// As extracts a *fault.Error from an error chain.
func As(err error) (*Error, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

// List is an ordered collection of faults with deterministic helpers used by
// manifests and verification output.
type List []*Error

// Add appends err. Non-fault errors are wrapped as internal pipeline faults.
func (l *List) Add(err error) {
	if err == nil {
		return
	}
	if fe, ok := As(err); ok {
		*l = append(*l, fe)
		return
	}
	fe := Wrap(StagePipeline, KindIO, err, "unclassified error")
	*l = append(*l, fe)
}

// Len returns the number of faults.
func (l List) Len() int { return len(l) }

// CountKind returns how many faults of kind k were recorded.
func (l List) CountKind(k Kind) int {
	n := 0
	for _, e := range l {
		if e.Kind == k {
			n++
		}
	}
	return n
}

// FirstOffset returns the lowest raw byte offset recorded, if any.
func (l List) FirstOffset() (int64, bool) {
	best := int64(-1)
	found := false
	for _, e := range l {
		if e.Offset < 0 {
			continue
		}
		if !found || e.Offset < best {
			best = e.Offset
			found = true
		}
	}
	return best, found
}

// Sorted returns a copy ordered by offset then stage and kind.
func (l List) Sorted() List {
	out := append(List(nil), l...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		if out[i].Stage != out[j].Stage {
			return out[i].Stage < out[j].Stage
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// Summary renders "kind:count" pairs, sorted, for terminal output.
func (l List) Summary() string {
	counts := map[Kind]int{}
	for _, e := range l {
		counts[e.Kind]++
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s:%d", k, counts[Kind(k)]))
	}
	return strings.Join(parts, ",")
}
