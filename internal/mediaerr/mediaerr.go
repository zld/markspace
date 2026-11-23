// Package mediaerr defines the error model shared by every avbatch stage.
//
// A media error never loses the location it was detected at: the segment
// ordinal, the box type and the byte offset are carried all the way from the
// CMAF parser to the index writer. Callers classify by Kind instead of
// matching on message text.
package mediaerr

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Kind is a stable, machine-readable classification of an error.
type Kind string

const (
	// Container / parsing failures.
	KindTruncatedBox   Kind = "truncated_box"
	KindInvalidBoxSize Kind = "invalid_box_size"
	KindMissingField   Kind = "missing_field"
	KindInvalidBox     Kind = "invalid_box"

	// Fragment ordering failures.
	KindSequenceGap     Kind = "sequence_gap"
	KindSequenceRepeat  Kind = "sequence_repeat"
	KindDuplicate       Kind = "duplicate_fragment"
	KindTfdtRegression  Kind = "tfdt_regression"
	KindUnsorted        Kind = "unsorted_sample"
	KindRTPWrap         Kind = "rtp_wrap"
	KindRTPRegression   Kind = "rtp_regression"
	KindClockDisorder   Kind = "clock_disorder"
	KindDriftOutOfRange Kind = "drift_out_of_range"

	// Segmentation / audio / fingerprint.
	KindGap             Kind = "gap"
	KindOverlap         Kind = "overlap"
	KindNoLegalBoundary Kind = "no_legal_boundary"
	KindSilenceWindow   Kind = "silence_window"
	KindHashCollision   Kind = "hash_collision"

	// Index / checkpoint.
	KindCorruptCheckpoint Kind = "corrupt_checkpoint"
	KindTruncatedIndex    Kind = "truncated_index"
	KindResumeMismatch    Kind = "resume_mismatch"
	KindChecksumMismatch  Kind = "checksum_mismatch"
	KindManifestOrder     Kind = "manifest_order"
)

// Error carries detection context. It implements error and unwraps Err.
type Error struct {
	Kind    Kind
	Segment int
	Track   uint32
	BoxType string
	Offset  int64
	Msg     string
	Err     error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Kind))
	b.WriteString(": segment=")
	b.WriteString(fmt.Sprint(e.Segment))
	if e.BoxType != "" {
		b.WriteString(" box=")
		b.WriteString(e.BoxType)
	}
	b.WriteString(" offset=")
	b.WriteString(fmt.Sprint(e.Offset))
	if e.Track != 0 {
		b.WriteString(" track=")
		b.WriteString(fmt.Sprint(e.Track))
	}
	if e.Msg != "" {
		b.WriteString(": ")
		b.WriteString(e.Msg)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// New builds an Error. segment is the zero-based fragment ordinal used by the
// rest of the pipeline; use -1 when the failure predates fragmentation.
func New(kind Kind, segment int, boxType string, offset int64, msg string) *Error {
	return &Error{Kind: kind, Segment: segment, BoxType: boxType, Offset: offset, Msg: msg}
}

// Wrap is New with a cause.
func Wrap(kind Kind, segment int, boxType string, offset int64, msg string, cause error) *Error {
	return &Error{Kind: kind, Segment: segment, BoxType: boxType, Offset: offset, Msg: msg, Err: cause}
}

// WithTrack returns a copy annotated with a track id.
func (e *Error) WithTrack(track uint32) *Error {
	c := *e
	c.Track = track
	return &c
}

// Classify returns the Kind of err, or "" when it is not a media error.
func Classify(err error) Kind {
	var me *Error
	if errors.As(err, &me) {
		return me.Kind
	}
	return ""
}

// Is reports whether err is a media error of the given kind.
func Is(err error, kind Kind) bool {
	return Classify(err) == kind
}

// Collect flattens err and every joined/wrapped media error beneath it.
// Duplicate pointers are reported once; order is deterministic.
func Collect(err error) []*Error {
	if err == nil {
		return nil
	}
	type key struct {
		kind    Kind
		segment int
		box     string
		offset  int64
		msg     string
	}
	seen := map[key]bool{}
	var out []*Error
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		var me *Error
		if errors.As(e, &me) {
			k := key{me.Kind, me.Segment, me.BoxType, me.Offset, me.Msg}
			if !seen[k] {
				seen[k] = true
				out = append(out, me)
			}
			if me.Err != nil {
				walk(me.Err)
				return
			}
		}
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range u.Unwrap() {
				walk(child)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Segment != out[j].Segment {
			return out[i].Segment < out[j].Segment
		}
		return out[i].Offset < out[j].Offset
	})
	return out
}

// Kinds returns the distinct kinds present in err, sorted for stable output.
func Kinds(err error) []Kind {
	seen := map[Kind]bool{}
	var out []Kind
	for _, e := range Collect(err) {
		if !seen[e.Kind] {
			seen[e.Kind] = true
			out = append(out, e.Kind)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
