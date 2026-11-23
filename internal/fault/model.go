// Package fault provides the single error taxonomy shared by scanning,
// recovery, audio and manifest code.  Every failure carries a stable category,
// a byte or tick offset where the problem starts, and an explicit
// recoverability decision so callers can degrade instead of guessing.
package fault

import (
	"errors"
	"fmt"
	"sort"
)

// Class groups errors by where they originate.  Values are stable and safe to
// print in machine-readable output.
type Class string

const (
	// ClassInput covers unreadable or unsupported input.
	ClassInput Class = "input"
	// ClassContainer covers MP4 structure problems.
	ClassContainer Class = "container"
	// ClassIndex covers sample index/table inconsistency.
	ClassIndex Class = "index"
	// ClassSample covers corrupt media payload bytes.
	ClassSample Class = "sample"
	// ClassTimeline covers clock and edit-list invariants.
	ClassTimeline Class = "timeline"
	// ClassDependency covers track graph problems.
	ClassDependency Class = "dependency"
	// ClassAudio covers resampling, layout and loudness problems.
	ClassAudio Class = "audio"
	// ClassManifest covers manifest parse/emit problems.
	ClassManifest Class = "manifest"
	// ClassLimit covers configured size/duration caps.
	ClassLimit Class = "limit"
)

// Recovery classifies whether processing may continue.
type Recovery uint8

const (
	// Recoverable: a degraded but well-defined output exists.
	Recoverable Recovery = iota + 1
	// Unrecoverable: the input cannot yield a trustworthy result.
	Unrecoverable
)

func (r Recovery) String() string {
	switch r {
	case Recoverable:
		return "recoverable"
	case Unrecoverable:
		return "unrecoverable"
	default:
		return "unknown"
	}
}

// OffsetKind says whether Offset is measured in bytes or clock ticks.
type OffsetKind string

const (
	OffsetNone OffsetKind = ""
	OffsetByte OffsetKind = "byte"
	OffsetTick OffsetKind = "tick"
)

// Fault is one classified problem.
type Fault struct {
	Class      Class
	Code       string
	Message    string
	Offset     int64
	OffsetKind OffsetKind
	Recovery   Recovery
	// TrackID is zero when the fault is not track-specific.
	TrackID uint32
	// Cause is the wrapped error, if any.
	Cause error
}

// Error implements error.
func (f Fault) Error() string {
	loc := ""
	switch f.OffsetKind {
	case OffsetByte:
		loc = fmt.Sprintf(" at byte %d", f.Offset)
	case OffsetTick:
		loc = fmt.Sprintf(" at tick %d", f.Offset)
	}
	tr := ""
	if f.TrackID != 0 {
		tr = fmt.Sprintf(" track=%d", f.TrackID)
	}
	return fmt.Sprintf("%s/%s%s%s: %s [%s]", f.Class, f.Code, tr, loc, f.Message, f.Recovery)
}

// Unwrap exposes the cause for errors.Is/errors.As.
func (f Fault) Unwrap() error { return f.Cause }

// Is implements the errors.Is matching protocol so callers can test with
// errors.Is(err, fault.OfClass(fault.ClassIndex, "")) or compare two faults.
func (f Fault) Is(target error) bool {
	switch t := target.(type) {
	case *matcher:
		if f.Class != t.class {
			return false
		}
		return t.code == "" || f.Code == t.code
	case Fault:
		return f.Class == t.Class && f.Code == t.Code
	}
	return false
}

// New builds a recoverable fault.
func New(class Class, code, msg string) Fault {
	return Fault{Class: class, Code: code, Message: msg, Recovery: Recoverable}
}

// Fatal builds an unrecoverable fault.
func Fatal(class Class, code, msg string) Fault {
	return Fault{Class: class, Code: code, Message: msg, Recovery: Unrecoverable}
}

// AtByte attaches a byte offset and keeps the recovery class.
func (f Fault) AtByte(off int64) Fault {
	f.Offset = off
	f.OffsetKind = OffsetByte
	return f
}

// AtTick attaches a tick offset.
func (f Fault) AtTick(off int64) Fault {
	f.Offset = off
	f.OffsetKind = OffsetTick
	return f
}

// OnTrack attaches a track identifier.
func (f Fault) OnTrack(id uint32) Fault {
	f.TrackID = id
	return f
}

// Because attaches the underlying cause.
func (f Fault) Because(err error) Fault {
	f.Cause = err
	return f
}

// AsRecoverable forces the recovery class; used by policy decisions.
func (f Fault) AsRecoverable() Fault {
	f.Recovery = Recoverable
	return f
}

// AsUnrecoverable forces the recovery class.
func (f Fault) AsUnrecoverable() Fault {
	f.Recovery = Unrecoverable
	return f
}

// Is reports whether the target is a Fault and matches class and code.  It
// lets callers write errors.Is(err, fault.OfClass(...)).
type matcher struct {
	class Class
	code  string
}

// OfClass matches any fault of the given class; when code is empty it matches
// any code.
func OfClass(class Class, code string) *matcher { return &matcher{class: class, code: code} }

func (m *matcher) Error() string {
	if m.code == "" {
		return string(m.class)
	}
	return string(m.class) + "/" + m.code
}

// Is implements the errors.Is matching protocol.
func (m *matcher) Is(target error) bool {
	var f Fault
	if !errors.As(target, &f) {
		return false
	}
	if f.Class != m.class {
		return false
	}
	return m.code == "" || f.Code == m.code
}

// Collector accumulates faults in deterministic order.
type Collector struct {
	faults []Fault
}

// Add records a fault.  Non-fault errors are wrapped as recoverable input
// faults so the collector never receives an unclassified error.
func (c *Collector) Add(err error) {
	if err == nil {
		return
	}
	var f Fault
	if errors.As(err, &f) {
		c.faults = append(c.faults, f)
		return
	}
	c.faults = append(c.faults, New(ClassInput, "other", err.Error()).Because(err))
}

// AddFault records a fault directly.
func (c *Collector) AddFault(f Fault) { c.faults = append(c.faults, f) }

// Len returns the number of recorded faults.
func (c *Collector) Len() int { return len(c.faults) }

// AnyUnrecoverable reports whether any recorded fault blocks processing.
func (c *Collector) AnyUnrecoverable() bool {
	for _, f := range c.faults {
		if f.Recovery == Unrecoverable {
			return true
		}
	}
	return false
}

// Faults returns a copy sorted by class, offset and code so summaries hash
// identically across repeated runs.
func (c *Collector) Faults() []Fault {
	out := make([]Fault, len(c.faults))
	copy(out, c.faults)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// Summary renders every fault as one line, in deterministic order.
func (c *Collector) Summary() []string {
	fs := c.Faults()
	lines := make([]string, len(fs))
	for i, f := range fs {
		lines[i] = f.Error()
	}
	return lines
}

// ExitCode maps the collected faults to the CLI contract:
// 3 unrecoverable input or dependency errors, 4 quality-only issues, 0 clean.
func (c *Collector) ExitCode() int {
	if c.AnyUnrecoverable() {
		return 3
	}
	if len(c.faults) > 0 {
		return 4
	}
	return 0
}
