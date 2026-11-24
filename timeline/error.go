package timeline

import "errors"

// Category classifies failures across subsystems so the CLI can map them to
// exit codes and reports can group related faults.
type Category string

const (
	// CatCorrupt marks input data that is truncated, malformed or fails a
	// checksum. It maps to exit code 2.
	CatCorrupt Category = "corrupt"
	// CatUnsupported marks valid-but-unsupported metadata such as an unknown
	// channel layout or color enumeration. It maps to exit code 2.
	CatUnsupported Category = "unsupported"
	// CatUnrecoverable marks loss (e.g. RTP gaps outside the replay window)
	// that cannot be reconstructed. It maps to exit code 2.
	CatUnrecoverable Category = "unrecoverable"
	// CatConfig marks invalid parameters and configuration conflicts.
	CatConfig Category = "config"
	// CatIO marks local storage and filesystem failures.
	CatIO Category = "io"
	// CatOverflow marks rejected integer arithmetic and size-limit breaches.
	CatOverflow Category = "overflow"
)

// Error carries a machine-readable category plus the track and byte-offset
// context a reviewer needs to relocate the fault in the source file.
type Error struct {
	Category Category
	Message  string
	TrackUID uint32 // 0 when the fault is not tied to a track
	Offset   int64  // -1 when no byte offset is known
	Wrap     error  // wrapped underlying error, may be nil
}

func (e *Error) Error() string {
	ctx := ""
	if e.TrackUID != 0 {
		ctx += " track=" + itoa(int64(e.TrackUID))
	}
	if e.Offset >= 0 {
		ctx += " offset=" + itoa(e.Offset)
	}
	if e.Wrap != nil {
		ctx += ": " + e.Wrap.Error()
	}
	return string(e.Category) + ": " + e.Message + ctx
}

func (e *Error) Unwrap() error { return e.Wrap }

// New builds a context-free error of the given category.
func New(cat Category, msg string) *Error {
	return &Error{Category: cat, Message: msg, Offset: -1}
}

// WithTrack attaches a track UID to an error.
func (e *Error) WithTrack(uid uint32) *Error { e.TrackUID = uid; return e }

// WithOffset attaches a byte offset to an error.
func (e *Error) WithOffset(off int64) *Error { e.Offset = off; return e }

// Wrap annotates the error with an underlying cause.
func (e *Error) Wrapf(err error) *Error { e.Wrap = err; return e }

// IsCategory reports whether err (or any wrapped error) carries cat.
func IsCategory(err error, cat Category) bool {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Category == cat
	}
	return false
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
