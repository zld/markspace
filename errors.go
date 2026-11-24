package mediacore

import (
	"errors"
	"fmt"
)

// ErrorKind is a stable classification for failures raised by mediacore.
// Every failure path in this module returns a *Error so callers (and the
// demo) can decide programmatically what to do.
type ErrorKind string

const (
	KindInvalidConfig ErrorKind = "invalid_config"
	KindContainer     ErrorKind = "container"
	KindTimestamp     ErrorKind = "timestamp"
	KindTrackMap      ErrorKind = "track_map"
	KindIndex         ErrorKind = "index"
	KindDigest        ErrorKind = "digest"
	KindTransport     ErrorKind = "transport"
	KindOverflow      ErrorKind = "overflow"
	KindIO            ErrorKind = "io"
	KindInternal      ErrorKind = "internal"
)

// Error is a classified failure. Op names the operation that failed and both
// Msg and Err are useful to humans; Kind is the machine-readable part.
type Error struct {
	Kind ErrorKind
	Op   string
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	switch {
	case e.Msg != "" && e.Err != nil:
		return fmt.Sprintf("%s: %s: %s: %v", e.Kind, e.Op, e.Msg, e.Err)
	case e.Msg != "":
		return fmt.Sprintf("%s: %s: %s", e.Kind, e.Op, e.Msg)
	case e.Err != nil:
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Op, e.Err)
	default:
		return fmt.Sprintf("%s: %s", e.Kind, e.Op)
	}
}

func (e *Error) Unwrap() error { return e.Err }

// Is lets errors.Is(err, target) compare on kind while the target may be a
// plain sentinel of the same kind.
func (e *Error) Is(target error) bool {
	var other *Error
	return errors.As(target, &other) && other.Kind == e.Kind
}

// fail constructs a classified error.
func fail(kind ErrorKind, op, format string, args ...interface{}) error {
	return &Error{Kind: kind, Op: op, Msg: fmt.Sprintf(format, args...)}
}

// wrapErr adds a classification to an underlying error without discarding it.
func wrapErr(kind ErrorKind, op string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Kind: kind, Op: op, Err: err}
}

// KindOf returns the classification of err, or "" when err is not classified.
func KindOf(err error) ErrorKind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// IsKind reports whether err (or anything it wraps) has the given kind.
func IsKind(err error, kind ErrorKind) bool {
	return KindOf(err) == kind
}

// ErrIndexStale is returned when a cached index no longer matches the input
// digest. Callers are expected to rebuild and retry.
var ErrIndexStale = &Error{Kind: KindIndex, Op: "cache", Msg: "cached index is stale for the current input digest"}

// GapState describes the lifecycle of a missing range in a recovered stream.
type GapState string

const (
	// GapPending means packets are still inside the reorder window and a
	// late arrival may still fill the hole.
	GapPending GapState = "pending"
	// GapMissing is terminal: the hole left the window and no media samples
	// may be invented for it.
	GapMissing GapState = "missing"
	// GapRecovered means the hole was filled by a late, previously missing
	// packet before it was declared terminal.
	GapRecovered GapState = "recovered"
)

// Gap records a contiguous missing RTP sequence range for one track.
type Gap struct {
	TrackID uint32
	Stream  string
	// FirstSeq and LastSeq are extended (wrap-unrolled) sequence numbers, so
	// ranges spanning a 16-bit wrap remain comparable.
	FirstSeq uint64
	LastSeq  uint64
	State    GapState
}

// Count returns the number of packets in the range.
func (g Gap) Count() uint64 {
	if g.LastSeq < g.FirstSeq {
		return 0
	}
	return g.LastSeq - g.FirstSeq + 1
}

func (g Gap) String() string {
	return fmt.Sprintf("track=%d stream=%s seq=[%d,%d] count=%d state=%s",
		g.TrackID, g.Stream, g.FirstSeq, g.LastSeq, g.Count(), g.State)
}

// GapLog is an ordered collection of gaps plus terminal statistics.
type GapLog struct {
	Gaps []Gap
}

// Add appends a gap and returns its index.
func (l *GapLog) Add(g Gap) int {
	l.Gaps = append(l.Gaps, g)
	return len(l.Gaps) - 1
}

// MissingCount returns the number of packets across all terminal gaps.
func (l *GapLog) MissingCount() uint64 {
	var n uint64
	for _, g := range l.Gaps {
		if g.State == GapMissing {
			n += g.Count()
		}
	}
	return n
}

// Terminal returns a copy of the log with any pending gaps declared missing.
func (l *GapLog) Terminal() *GapLog {
	out := &GapLog{Gaps: make([]Gap, len(l.Gaps))}
	copy(out.Gaps, l.Gaps)
	for i := range out.Gaps {
		if out.Gaps[i].State == GapPending {
			out.Gaps[i].State = GapMissing
		}
	}
	return out
}

// Resolved reports whether all gaps reached a terminal state.
func (l *GapLog) Resolved() bool {
	for _, g := range l.Gaps {
		if g.State == GapPending {
			return false
		}
	}
	return true
}
