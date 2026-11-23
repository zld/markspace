package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Stage identifies the pipeline phase that produced an error.
type Stage string

const (
	StageProbe    Stage = "probe"
	StageIndex    Stage = "index"
	StageTimeline Stage = "timeline"
	StageManifest Stage = "manifest"
	StageLadder   Stage = "ladder"
	StageRunner   Stage = "runner"
)

// Code is a stable machine-readable error classification.
type Code string

const (
	CodeTruncatedBox   Code = "truncated_box"
	CodeIllegalLength  Code = "illegal_length"
	CodeTimestampBack  Code = "timestamp_regression"
	CodeMissingTrack   Code = "missing_track"
	CodeOverflow       Code = "overflow"
	CodeCorruptIndex   Code = "corrupt_index"
	CodeCheckpointBad  Code = "corrupt_checkpoint"
	CodeInputChanged   Code = "input_changed"
	CodeUnsupported    Code = "unsupported"
	CodeLimitExceeded  Code = "limit_exceeded"
	CodeAlignTolerance Code = "alignment_tolerance"
)

// ByteRange is a half-open [Offset, Offset+Length) interval inside one input.
type ByteRange struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
}

// PipelineError is the single structured error type flowing through every
// stage: it always carries the stage, a stable code, the originating input
// name, the responsible track (empty when global), and a byte interval.
type PipelineError struct {
	Stage     Stage     `json:"stage"`
	Code      Code      `json:"code"`
	Input     string    `json:"input"`
	TrackID   uint32    `json:"track_id,omitempty"`
	ByteRange ByteRange `json:"byte_range"`
	Message   string    `json:"message"`
}

func (e *PipelineError) Error() string {
	track := ""
	if e.TrackID != 0 {
		track = fmt.Sprintf(" track=%d", e.TrackID)
	}
	return fmt.Sprintf("%s/%s: %s [%s%s bytes=[%d,+%d)]",
		e.Stage, e.Code, e.Message, e.Input, track, e.ByteRange.Offset, e.ByteRange.Length)
}

// Errors aggregates several structured errors. The zero value is ready to use.
// Sorting is by earliest input offset, then input name, then code, so output
// is deterministic across repeated runs.
type Errors struct {
	items []*PipelineError
}

// Add appends an error. Nil inputs are ignored.
func (es *Errors) Add(e *PipelineError) {
	if e != nil {
		es.items = append(es.items, e)
	}
}

// Newf builds and appends a structured error in one call.
func (es *Errors) Newf(st Stage, code Code, input string, track uint32, br ByteRange, format string, args ...any) {
	es.Add(&PipelineError{
		Stage:     st,
		Code:      code,
		Input:     input,
		TrackID:   track,
		ByteRange: br,
		Message:   fmt.Sprintf(format, args...),
	})
}

// Len reports how many errors were collected.
func (es *Errors) Len() int { return len(es.items) }

// Sorted returns the collected errors ordered by earliest input offset, then
// input name, then code. The receiver is not modified.
func (es *Errors) Sorted() []*PipelineError {
	out := make([]*PipelineError, len(es.items))
	copy(out, es.items)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ByteRange.Offset != b.ByteRange.Offset {
			return a.ByteRange.Offset < b.ByteRange.Offset
		}
		if a.Input != b.Input {
			return a.Input < b.Input
		}
		return a.Code < b.Code
	})
	return out
}

// Err converts the collection into a standard error, or nil when empty.
func (es *Errors) Err() error {
	if len(es.items) == 0 {
		return nil
	}
	sorted := es.Sorted()
	parts := make([]string, len(sorted))
	for i, e := range sorted {
		parts[i] = e.Error()
	}
	return fmt.Errorf("%d pipeline error(s): %s", len(sorted), strings.Join(parts, "; "))
}

// First returns the earliest error by input offset, or nil.
func (es *Errors) First() *PipelineError {
	if len(es.items) == 0 {
		return nil
	}
	return es.Sorted()[0]
}

// AsPipeline extracts the first *PipelineError from any wrapped error.
func AsPipeline(err error) (*PipelineError, bool) {
	var pe *PipelineError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}
