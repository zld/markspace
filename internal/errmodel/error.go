// Package errmodel defines stable, cross-module error codes.
//
// Code ranges encode the originating subsystem and also map to the process
// exit classification used by cmd/assetcheck:
//
//	1xxx  WAV/container format errors   -> exit 3 (input format error)
//	2xxx  stream/interval analysis      -> 3 when structural, 2 on budget
//	3xxx  time index                    -> 4 when the store cannot commit
//	4xxx  fingerprint cache             -> 4 when persistence fails
//	5xxx  WAL / transaction             -> exit 4
//	6xxx  business threshold violations -> exit 2
package errmodel

import "fmt"

// Category is the coarse failure class used for exit-code mapping.
type Category int

const (
	CatOK Category = iota
	CatFormat  // input cannot be parsed reliably
	CatStream  // bitstream/scan anomaly that still produced findings
	CatIndex   // index construction/validation problem
	CatCache   // cache persistence problem
	CatTxn     // WAL, commit or recovery failure
	CatBudget  // analysis succeeded but a business threshold was exceeded
	CatInternal
)

// Code is a stable numeric error identifier printed in reports and tests.
type Code int

const (
	OK Code = 0

	// 1xxx: WAV / RIFF / container format.
	ErrBadRIFF            Code = 1001
	ErrBadWAVE            Code = 1002
	ErrTruncatedHeader    Code = 1003
	ErrNoFmtChunk         Code = 1004
	ErrBadFmtSize         Code = 1005
	ErrUnsupportedFormat  Code = 1006
	ErrBadChannels        Code = 1007
	ErrBadSampleRate      Code = 1008
	ErrBadBitDepth        Code = 1009
	ErrBlockAlignMismatch Code = 1010
	ErrByteRateMismatch   Code = 1011
	ErrChunkOutOfBounds   Code = 1012
	ErrChunkTruncated     Code = 1013
	ErrNonFrameTail       Code = 1014
	ErrMissingDataChunk   Code = 1015
	ErrMetadataEncoding   Code = 1016

	// 2xxx: stream blocks / intervals.
	ErrBlockChecksum   Code = 2001
	ErrTruncatedBlock  Code = 2002
	ErrDuplicateBoundary Code = 2003
	ErrOverlapRange    Code = 2004
	ErrScanBudget      Code = 2005
	ErrClock           Code = 2006

	// 3xxx: time index.
	ErrDuplicateKey  Code = 3001
	ErrIndexChecksum Code = 3002
	ErrIndexSchema   Code = 3003

	// 4xxx: cache.
	ErrCacheChecksum Code = 4001
	ErrCacheSchema   Code = 4002

	// 5xxx: WAL / transactions.
	ErrWALWrite       Code = 5001
	ErrWALCorrupt     Code = 5002
	ErrInjected       Code = 5003
	ErrCommit         Code = 5004
	ErrRecovery       Code = 5005
	ErrStagingVisible Code = 5006

	// 6xxx: business thresholds (used as verdict codes).
	ErrThresholdSampleRate Code = 6001
	ErrThresholdChannels   Code = 6002
	ErrThresholdDuration   Code = 6003
	ErrThresholdMaxGap     Code = 6004
	ErrThresholdCorrupt    Code = 6005
)

// Info is one reportable, offset-tagged error or finding.
type Error struct {
	Code   Code     `json:"code"`
	Offset int64    `json:"offset"` // byte offset, -1 when not applicable
	Frame  int64    `json:"frame"`  // sample frame, -1 when not applicable
	Msg    string   `json:"message"`
}

// New builds an Error with offset/frame marked unavailable.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Offset: -1, Frame: -1, Msg: fmt.Sprintf(format, args...)}
}

// AtOffset attaches a byte offset.
func (e *Error) AtOffset(off int64) *Error { e.Offset = off; return e }

// AtFrame attaches a sample frame number.
func (e *Error) AtFrame(f int64) *Error { e.Frame = f; return e }

func (e *Error) Error() string {
	switch {
	case e.Offset >= 0 && e.Frame >= 0:
		return fmt.Sprintf("[E%04d] offset=%d frame=%d: %s", e.Code, e.Offset, e.Frame, e.Msg)
	case e.Offset >= 0:
		return fmt.Sprintf("[E%04d] offset=%d: %s", e.Code, e.Offset, e.Msg)
	case e.Frame >= 0:
		return fmt.Sprintf("[E%04d] frame=%d: %s", e.Code, e.Frame, e.Msg)
	default:
		return fmt.Sprintf("[E%04d] %s", e.Code, e.Msg)
	}
}

// Category maps a code to its coarse category.
func (c Code) Category() Category {
	switch {
	case c == OK:
		return CatOK
	case c >= 1000 && c < 2000:
		return CatFormat
	case c >= 2000 && c < 3000:
		return CatStream
	case c >= 3000 && c < 4000:
		return CatIndex
	case c >= 4000 && c < 5000:
		return CatCache
	case c >= 5000 && c < 6000:
		return CatTxn
	case c >= 6000 && c < 7000:
		return CatBudget
	default:
		return CatInternal
	}
}

// ExitCode maps an error to the acceptance exit-code contract.
// Budget violations map to 2, format/stream parse failures to 3 and
// transaction/recovery failures to 4. Unknown errors map to 1.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if e, ok := AsError(err); ok {
		switch e.Code.Category() {
		case CatBudget:
			return 2
		case CatFormat:
			return 3
		case CatTxn, CatIndex, CatCache:
			return 4
		default:
			return 1
		}
	}
	return 1
}

// AsError extracts the first *Error from err, including wrapped errors.
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
