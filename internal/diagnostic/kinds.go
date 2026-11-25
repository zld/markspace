package diagnostic

import "errors"

// Stable error kinds allow CLI exit-code mapping and tests to avoid matching prose.
var (
	ErrZeroTimescale   = errors.New("zero-timescale")
	ErrMissingDuration = errors.New("missing-duration")
	ErrOutOfRange      = errors.New("out-of-range-index")
	ErrTimeRegression  = errors.New("time-regression")
	ErrOverflow        = errors.New("rational-overflow")
	ErrZeroDenominator = errors.New("zero-denominator")
	ErrMisaligned      = errors.New("representation-misaligned")
	ErrTruncated       = errors.New("truncated-segment")
	ErrIllegalSample   = errors.New("illegal-sample-length")
	ErrCancelled       = errors.New("cancelled")
	ErrCheckpoint      = errors.New("checkpoint-mismatch")
	ErrPathEscape      = errors.New("path-outside-workdir")
	ErrDuplicate       = errors.New("duplicate-entry")
)

// Exit codes required by the acceptance contract.
const (
	ExitOK              = 0
	ExitGeneric         = 1
	ExitCorrupt         = 2
	ExitCheckpointInput = 3
)

func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	le, ok := As(err)
	if ok {
		switch {
		case errors.Is(le.Kind, ErrCheckpoint):
			return ExitCheckpointInput
		case errors.Is(le.Kind, ErrTruncated),
			errors.Is(le.Kind, ErrIllegalSample),
			errors.Is(le.Kind, ErrZeroTimescale),
			errors.Is(le.Kind, ErrMissingDuration),
			errors.Is(le.Kind, ErrTimeRegression):
			return ExitCorrupt
		}
	}
	return ExitGeneric
}
