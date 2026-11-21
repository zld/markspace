package pipeline

import (
	"errors"
	"fmt"
	"os"

	"bitrack/internal/model"
)

// Injector hooks the deterministic failure modes required for recovery tests.
// The zero value path (NoopInjector) never fires.
type Injector interface {
	OnEncode(seg *model.Segment) error
	OnValidate(seg *model.Segment) error
}

// NoopInjector disables injection.
type NoopInjector struct{}

func (NoopInjector) OnEncode(*model.Segment) error   { return nil }
func (NoopInjector) OnValidate(*model.Segment) error { return nil }

// Fault kinds understood by FaultInjector. They map to the --inject flag.
const (
	FaultDiskFail    = "disk-fail"
	FaultFFmpegFail  = "ffmpeg-fail"
	FaultBadBaseline = "bad-baseline"
)

// PermanentError marks a failure that must not be retried: injected faults are
// deterministic, so a retry would either fail identically or (for one-shot
// injections) silently succeed and hide the fault. It satisfies the scheduler's
// permanentError interface via IsPermanent, with no import cycle.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string     { return e.Err.Error() }
func (e *PermanentError) Unwrap() error     { return e.Err }
func (e *PermanentError) IsPermanent() bool { return true }

// IsPermanent reports whether err wraps a PermanentError.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// FaultInjector triggers a named fault once. A TargetSegment of -1 means
// "fire on the first segment that reaches encoding", which is required under
// concurrent workers where dispatch order is not deterministic.
type FaultInjector struct {
	Kind          string
	TargetSegment int
	fired         bool
	workDir       string
}

// NewFaultInjector builds an injector for one run.
func NewFaultInjector(kind string, targetSegment int, workDir string) *FaultInjector {
	return &FaultInjector{Kind: kind, TargetSegment: targetSegment, workDir: workDir}
}

// OnEncode fires encode-time faults: disk write failure or encoder failure.
// Both are returned as PermanentError so the scheduler does not retry them.
func (f *FaultInjector) OnEncode(seg *model.Segment) error {
	if f == nil || f.fired {
		return nil
	}
	if f.TargetSegment >= 0 && seg.Index != f.TargetSegment {
		return nil
	}
	switch f.Kind {
	case FaultDiskFail:
		f.fired = true
		// Remove the segments directory and replace it with a regular file,
		// so the next output open fails with ENOTDIR — a locatable disk error.
		dir := f.workDir + "/segments"
		_ = os.RemoveAll(dir)
		if err := os.WriteFile(dir, []byte("blocked"), 0o644); err != nil {
			return &PermanentError{Err: fmt.Errorf("disk-fault setup failed: %w", err)}
		}
		return &PermanentError{Err: fmt.Errorf(
			"injected disk write failure: segments path %s is not a directory", dir)}
	case FaultFFmpegFail:
		f.fired = true
		return &PermanentError{Err: fmt.Errorf(
			"injected encoder failure (simulated non-zero ffmpeg exit)")}
	}
	return nil
}

// OnValidate fires validation-time faults. The bad-baseline case is driven by
// the CLI before scheduling, so this hook currently has no matching kind but
// remains the extension point.
func (f *FaultInjector) OnValidate(seg *model.Segment) error {
	return nil
}

// Fired reports whether the single-shot fault has triggered.
func (f *FaultInjector) Fired() bool { return f != nil && f.fired }

// BaselineCorruptor implements the bad-baseline fault outside the scheduler:
// it rewrites the baseline file with invalid JSON before quality comparison.
func BaselineCorruptor(path string) error {
	if err := os.WriteFile(path, []byte("{deliberately-corrupt-baseline"), 0o644); err != nil {
		return fmt.Errorf("inject bad-baseline: %w", err)
	}
	return nil
}
