package media

import (
	"errors"
	"fmt"
	"strings"
)

type Kind string

const (
	KindInput    Kind = "input"
	KindQuality  Kind = "quality"
	KindBudget   Kind = "budget"
	KindCanceled Kind = "canceled"
)

// Error is the structured error shared by ingest, merge, render and output.
// Optional coordinates identify a track, packet/record or file byte offset.
type Error struct {
	Kind        Kind
	Phase       string
	TrackID     string
	PacketIndex *int
	RecordIndex *int
	ByteOffset  *int64
	Err         error
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mediasync error kind=%s exit=%d phase=%s", e.Kind, e.ExitCode(), e.Phase)
	if e.TrackID != "" {
		fmt.Fprintf(&b, " track=%q", e.TrackID)
	}
	if e.PacketIndex != nil {
		fmt.Fprintf(&b, " packet_index=%d", *e.PacketIndex)
	}
	if e.RecordIndex != nil {
		fmt.Fprintf(&b, " record_index=%d", *e.RecordIndex)
	}
	if e.ByteOffset != nil {
		fmt.Fprintf(&b, " byte_offset=%d", *e.ByteOffset)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) ExitCode() int { return ExitCodeForKind(e.Kind) }

func ExitCodeForKind(kind Kind) int {
	switch kind {
	case KindInput:
		return 2
	case KindQuality:
		return 3
	case KindBudget:
		return 4
	case KindCanceled:
		return 130
	default:
		return 1
	}
}

func InputError(phase, track string, err error) *Error {
	return &Error{Kind: KindInput, Phase: phase, TrackID: track, Err: err}
}

func QualityError(phase, track string, err error) *Error {
	return &Error{Kind: KindQuality, Phase: phase, TrackID: track, Err: err}
}

func BudgetError(phase string, err error) *Error {
	return &Error{Kind: KindBudget, Phase: phase, Err: err}
}

func CanceledError(phase, track string) *Error {
	return &Error{Kind: KindCanceled, Phase: phase, TrackID: track, Err: errors.New("processing canceled")}
}

func OverflowError(phase, track string, err error) *Error {
	return &Error{Kind: KindInput, Phase: phase, TrackID: track, Err: fmt.Errorf("timestamp overflow: %w", err)}
}

func (e *Error) WithPacket(index int) *Error {
	cp := *e
	cp.PacketIndex = &index
	return &cp
}

func (e *Error) WithRecord(index int) *Error {
	cp := *e
	cp.RecordIndex = &index
	return &cp
}

func (e *Error) WithOffset(offset int64) *Error {
	cp := *e
	cp.ByteOffset = &offset
	return &cp
}

// AsError returns the structured *Error contained in err, if any.
func AsError(err error) *Error {
	var fe *Error
	if errors.As(err, &fe) {
		return fe
	}
	return nil
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if fe := AsError(err); fe != nil {
		return fe.ExitCode()
	}
	return 1
}
