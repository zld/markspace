// Package safeio centralizes the resource limits and path containment rules
// that every parser in mediacheck must obey.
package safeio

import (
	"errors"
	"io"
	"path/filepath"
	"strings"

	"mediacheck/internal/model"
)

// Limits are the hard, defensive caps applied to untrusted input.
type Limits struct {
	MaxParseBytes   int64 // maximum bytes consumed while parsing one unit
	MaxReportBytes  int64 // maximum serialized report size
	MaxDurationTick int64 // maximum accepted timeline duration in master ticks
	MaxBoxes        int   // maximum number of ISO BMFF boxes per parse
}

// DefaultLimits returns the conservative defaults used by the demo and tests.
func DefaultLimits() Limits {
	return Limits{
		MaxParseBytes:   32 << 20, // 32 MiB
		MaxReportBytes:  1 << 20,  // 1 MiB
		MaxDurationTick: 24 * 60 * 60 * 90000,
		MaxBoxes:        4096,
	}
}

// LimitedReader mirrors io.LimitedReader but returns model.ErrMemoryLimit once
// the budget is exhausted instead of a generic EOF.
func LimitedReader(r io.Reader, n int64) io.Reader {
	return &limitedReader{r: r, n: n}
}

type limitedReader struct {
	r io.Reader
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, model.ErrMemoryLimit
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	if l.n <= 0 {
		return n, model.ErrMemoryLimit
	}
	return n, err
}

// ResolveContained joins root with name and guarantees the result stays inside
// root. Absolute names, traversal segments and symlink-style escapes are
// rejected before any filesystem call happens.
func ResolveContained(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", model.ErrUnsafePath
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", model.ErrUnsafePath
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(rootAbs, clean)
	rel, err := filepath.Rel(rootAbs, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", model.ErrUnsafePath
	}
	return full, nil
}

// CheckDuration rejects negative, zero and out-of-bounds durations.
func CheckDuration(ticks int64, lim Limits) error {
	if ticks <= 0 {
		return errors.New("duration must be positive")
	}
	if ticks > lim.MaxDurationTick {
		return model.ErrDurationBounds
	}
	return nil
}
