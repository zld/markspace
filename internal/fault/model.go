// Package fault provides the single structured error model shared by the
// transport-stream parser, the audio pipeline and the recovery machinery.
//
// A Fault always names the pipeline stage in which it was observed and
// carries either an input byte offset, a sample position, or both.  The
// Recoverable flag states whether processing may continue while preserving
// the timeline (a gap is recorded) or whether the operation must abort.
package fault

import (
	"fmt"
	"strings"
)

// Stage identifies the pipeline stage that produced a fault.
type Stage string

const (
	StageFixture  Stage = "fixture"
	StageParse    Stage = "parse"
	StageAudio    Stage = "audio"
	StageManifest Stage = "manifest-stage"
	StageVerify   Stage = "verify"
	StageCommit   Stage = "commit"
	StageRollback Stage = "rollback"
	StageState    Stage = "state"
	StageCLI      Stage = "cli"
)

// Kind is a stable machine-readable classification of a fault.
type Kind string

const (
	KindTruncated   Kind = "truncated-ts"
	KindSyncByte    Kind = "sync-byte"
	KindCCDuplicate Kind = "cc-duplicate"
	KindCCJump      Kind = "cc-jump"
	KindPSIUpdate   Kind = "psi-update"
	KindPSIChecksum Kind = "psi-checksum"
	KindPSIInvalid  Kind = "psi-invalid"
	KindPCR         Kind = "pcr-discontinuity"
	KindRTPInvalid  Kind = "rtp-invalid"
	KindRTPGap      Kind = "rtp-gap"
	KindAudioGap    Kind = "audio-gap"
	KindInvalidArg  Kind = "invalid-parameter"
	KindUnknownVer  Kind = "unknown-version"
	KindMissing     Kind = "missing-field"
	KindDigest      Kind = "digest-mismatch"
	KindPath        Kind = "path-escape"
	KindIO          Kind = "io"
	KindInjected    Kind = "injected-failure"
)

// Fault is the structured error record shared across all subsystems.
type Fault struct {
	Stage       Stage  `json:"stage"`
	Kind        Kind   `json:"kind"`
	Offset      int64  `json:"offset"`
	Sample      int64  `json:"sample"`
	Recoverable bool   `json:"recoverable"`
	Message     string `json:"message"`
}

// New builds a fault without a position.  Offset and Sample default to -1,
// meaning "not applicable".
func New(stage Stage, kind Kind, format string, args ...interface{}) *Fault {
	return &Fault{
		Stage:   stage,
		Kind:    kind,
		Offset:  -1,
		Sample:  -1,
		Message: fmt.Sprintf(format, args...),
	}
}

// AtOffset records the input byte offset at which the fault was observed.
func (f *Fault) AtOffset(offset int64) *Fault {
	f.Offset = offset
	return f
}

// AtSample records the media sample position at which the fault was observed.
func (f *Fault) AtSample(sample int64) *Fault {
	f.Sample = sample
	return f
}

// Recover marks whether processing can continue.
func (f *Fault) Recover(ok bool) *Fault {
	f.Recoverable = ok
	return f
}

func (f *Fault) Error() string {
	var b strings.Builder
	b.WriteString(string(f.Stage))
	b.WriteString("/")
	b.WriteString(string(f.Kind))
	if f.Offset >= 0 {
		fmt.Fprintf(&b, " offset=%d", f.Offset)
	}
	if f.Sample >= 0 {
		fmt.Fprintf(&b, " sample=%d", f.Sample)
	}
	fmt.Fprintf(&b, " recoverable=%t: %s", f.Recoverable, f.Message)
	return b.String()
}
