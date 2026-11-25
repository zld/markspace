// Package ingest reads local synthetic media inputs while enforcing task
// memory and input-size limits.
package ingest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/fingerprint"
)

const (
	// DefaultMaxFileBytes is intentionally small for the local synthetic
	// media task. Fixtures are measured in KiB, not real video bitstreams.
	DefaultMaxFileBytes int64 = 16 << 20
	DefaultTaskMemory   int64 = 64 << 20
)

// Budget is a best-effort atomic allocation budget for one task.
type Budget struct {
	limit int64
	used  atomic.Int64
}

func NewBudget(limit int64) *Budget {
	if limit <= 0 {
		limit = DefaultTaskMemory
	}
	return &Budget{limit: limit}
}

func (b *Budget) Take(n int64) error {
	if n < 0 {
		return fault.E(fault.StageIngest, fault.CodeInvalidArgument, "negative memory reservation")
	}
	for {
		old := b.used.Load()
		if old+n > b.limit {
			return fault.E(fault.StageIngest, fault.CodeMemoryLimit,
				fmt.Sprintf("task memory limit exceeded: used=%d requested=%d limit=%d", old, n, b.limit))
		}
		if b.used.CompareAndSwap(old, old+n) {
			return nil
		}
	}
}

func (b *Budget) Release(n int64) {
	b.used.Add(-n)
}

func (b *Budget) Used() int64  { return b.used.Load() }
func (b *Budget) Limit() int64 { return b.limit }

// Source is a fully read input plus provenance.
type Source struct {
	ID       string           `json:"id"`
	Path     string           `json:"path"`
	Size     int64            `json:"size"`
	Hash     fingerprint.Hash `json:"hash"`
	Data     []byte           `json:"-"`
	Budget   *Budget          `json:"-"`
	released bool
}

type ReadOptions struct {
	ID       string
	MaxBytes int64
	Budget   *Budget
}

func ReadFile(path string, opts ReadOptions) (*Source, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxFileBytes
	}
	budget := opts.Budget
	if budget == nil {
		budget = NewBudget(DefaultTaskMemory)
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, fault.Wrap(fault.StageIngest, fault.CodeReadFailed, err, "stat input",
			fault.WithPath(path))
	}
	if st.Size() > opts.MaxBytes {
		return nil, fault.E(fault.StageIngest, fault.CodeMemoryLimit,
			fmt.Sprintf("input size %d exceeds limit %d", st.Size(), opts.MaxBytes),
			fault.WithPath(path), fault.WithOffset(0, st.Size()))
	}
	if err := budget.Take(st.Size()); err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		budget.Release(st.Size())
		return nil, fault.Wrap(fault.StageIngest, fault.CodeReadFailed, err, "open input",
			fault.WithPath(path))
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, opts.MaxBytes+1))
	if err != nil {
		budget.Release(st.Size())
		return nil, fault.Wrap(fault.StageIngest, fault.CodeReadFailed, err, "read input",
			fault.WithPath(path))
	}
	if int64(len(data)) > opts.MaxBytes {
		budget.Release(int64(len(data)))
		return nil, fault.E(fault.StageIngest, fault.CodeMemoryLimit,
			fmt.Sprintf("input exceeds limit %d", opts.MaxBytes), fault.WithPath(path))
	}

	h := fingerprint.Bytes(data)
	id := opts.ID
	if id == "" {
		id = filepath.Base(path)
	}
	return &Source{
		ID:     id,
		Path:   path,
		Size:   int64(len(data)),
		Hash:   h,
		Data:   data,
		Budget: budget,
	}, nil
}

func (s *Source) Release() {
	if s == nil || s.released || s.Budget == nil {
		return
	}
	s.Budget.Release(s.Size)
	s.released = true
}

// ValidateRange verifies [offset, offset+length) inside data. It never clamps.
func ValidateRange(data []byte, offset, length int64) error {
	if offset < 0 || length < 0 {
		return fault.E(fault.StageIngest, fault.CodeOffsetOutOfRange,
			fmt.Sprintf("negative offset/length: offset=%d length=%d", offset, length),
			fault.WithOffset(offset, length))
	}
	end, overflow := addChecked(offset, length)
	if overflow || end > int64(len(data)) {
		return fault.E(fault.StageIngest, fault.CodeOffsetOutOfRange,
			fmt.Sprintf("range [%d,%d) exceeds input size %d", offset, offset+length, len(data)),
			fault.WithOffset(offset, length))
	}
	return nil
}

func addChecked(a, b int64) (int64, bool) {
	if b < 0 {
		return 0, true
	}
	if a > (1<<63-1)-b {
		return 0, true
	}
	return a + b, false
}

// EnsureWorkDir creates only the requested output directory.
func EnsureWorkDir(path string) error {
	if path == "" {
		return errors.New("work directory is required")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fault.Wrap(fault.StageIngest, fault.CodeReadFailed, err, "create work directory",
			fault.WithPath(path))
	}
	return nil
}
