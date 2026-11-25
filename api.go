// Package mediacore is the public reusable entry point for the processing
// kernel. The detailed implementations live under internal packages; this
// package re-exports the stable types and high-level functions used by other
// Go programs.
package mediacore

import (
	"github.com/zld/mediacore/internal/audio"
	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/pipeline"
	"github.com/zld/mediacore/internal/segment"
	"github.com/zld/mediacore/internal/store"
	"github.com/zld/mediacore/internal/trace"
)

// Unified time and interval types.
type (
	NS       = model.NS
	Interval = model.Interval
)

// NSPerSecond is the common nanosecond base.
const NSPerSecond = model.NSPerSecond

// Pipeline types.
type (
	Options = pipeline.Options
	Result  = pipeline.Result
)

// Segmentation types.
type (
	Segment       = segment.Segment
	SourceRef     = segment.SourceRef
	SegmentParams = segment.Params
	Cut           = segment.Cut
	Transition    = segment.Transition
)

// Audio types.
type (
	SilenceConfig   = audio.Config
	SilenceInterval = audio.Interval
)

// DefaultSilenceConfig returns the default amplitude/min-duration settings.
func DefaultSilenceConfig() SilenceConfig { return audio.DefaultConfig() }

// Store types.
type (
	Source     = store.Source
	DerivedKey = store.Key
	Cache      = store.Cache
)

// SourceFromBytes creates an immutable digest-addressed source.
func SourceFromBytes(data []byte, name string) *Source {
	return store.SourceFromBytes(data, name)
}

// OpenSource opens and hashes a read-only local source.
func OpenSource(path string) (*Source, error) { return store.OpenSource(path) }

// NewCache opens/creates a derived-artifact cache directory.
func NewCache(dir string) (*Cache, error) { return store.NewCache(dir) }

// Error location types.
type (
	Error = trace.Error
	Code  = trace.Code
	Stage = trace.Stage
)

// Stable error codes.
const (
	CodeInvalidArgument      = trace.CodeInvalidArgument
	CodeBoxLengthOutOfBounds = trace.CodeBoxLengthOutOfBounds
	CodeTruncated            = trace.CodeTruncated
	CodeMissingIndex         = trace.CodeMissingIndex
	CodeTimestampRegression  = trace.CodeTimestampRegression
	CodeEmptyTrack           = trace.CodeEmptyTrack
	CodeCutPointCorrupt      = trace.CodeCutPointCorrupt
	CodePayloadMismatch      = trace.CodePayloadMismatch
	CodeUnsupported          = trace.CodeUnsupported
	CodeInternal             = trace.CodeInternal
)

// Stable processing stages.
const (
	StageIngest      = trace.StageIngest
	StageRTP         = trace.StageRTP
	StageRecover     = trace.StageRecover
	StageNormalize   = trace.StageNormalize
	StageCut         = trace.StageCut
	StageAudio       = trace.StageAudio
	StageFingerprint = trace.StageFingerprint
	StageSource      = trace.StageSource
	StageDerived     = trace.StageDerived
)

// AsError finds the unified located error in err's chain.
func AsError(err error) (*Error, bool) { return trace.As(err) }

// HasCode reports whether err's chain contains code.
func HasCode(err error, code Code) bool { return trace.HasCode(err, code) }

// Process runs the kernel for an immutable source.
func Process(src *Source, opts Options) (*Result, error) {
	return pipeline.Process(src, opts)
}

// ProcessBytes is a convenience wrapper for in-memory MP4 data.
func ProcessBytes(data []byte, opts Options) (*Result, error) {
	return pipeline.Process(store.SourceFromBytes(data, "memory.mp4"), opts)
}

// ProcessFile opens path as an immutable source and runs the kernel.
func ProcessFile(path string, opts Options) (*Result, error) {
	src, err := store.OpenSource(path)
	if err != nil {
		return nil, err
	}
	return pipeline.Process(src, opts)
}
