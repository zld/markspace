// Package plan decides whether a run can fit inside a hard memory budget
// before any work starts. A rejected plan means no checkpoint is written and
// no segment is processed, so a failed admission leaves no partial state.
package plan

import (
	"errors"
	"tsindex/internal/ingest"
)

// Overhead constants are deliberately conservative upper bounds so the real
// working set stays below the accepted budget.
const (
	bytesPerPacket      = ingest.PacketSize          // 188 raw bytes
	defaultRecordOverhead = 96                       // header, timestamps, maps per record
	sidecarBufferBytes  = 64 * 1024                  // uuid payload staging per worker
	hasherStateBytes    = 256                        // two rolling SHA-256 states
	checkpointBytes     = 128 * 1024                 // checkpoint staging ceiling
	readerBufferBytes   = 64 * ingest.PacketSize     // StreamReader bufio buffer
)

// Params are the candidate knobs the planner evaluates.
type Params struct {
	PacketsPerSegment int
	Workers           int
}

// Budget is the accepted resource envelope for a run.
type Budget struct {
	BytesLimit        int64
	PacketsPerSegment int
	Workers           int
	// PerWorkerBytes is the high-water mark each goroutine may allocate.
	PerWorkerBytes int64
	// TotalBytes is the projected peak across every concurrent component.
	TotalBytes int64
}

// ErrBudgetExceeded is returned when no candidate parameter set fits. It is
// unrecoverable: callers must exit without writing a checkpoint.
var ErrBudgetExceeded = errors.New("plan: memory budget exceeded for all candidates")

// Planner evaluates candidate parameter sets against a byte budget.
type Planner struct {
	limit int64
}

// NewPlanner bounds every accepted plan to limitBytes of working memory.
func NewPlanner(limitBytes int64) *Planner { return &Planner{limit: limitBytes} }

// segmentFootprint is the retained size of one fully buffered segment:
// raw packet bytes plus the per-record overhead, shared by the worker while
// hashing and keyframe scanning.
func segmentFootprint(packetsPerSegment int) int64 {
	return int64(packetsPerSegment) * (bytesPerPacket + defaultRecordOverhead)
}

// Evaluate tests the requested parameters exactly as given. It returns the
// accepted budget or ErrBudgetExceeded without side effects.
func (p *Planner) Evaluate(params Params) (*Budget, error) {
	if params.PacketsPerSegment <= 0 || params.Workers <= 0 {
		return nil, errors.New("plan: segment size and workers must be positive")
	}
	seg := segmentFootprint(params.PacketsPerSegment)
	perWorker := seg + sidecarBufferBytes + hasherStateBytes
	total := readerBufferBytes +
		int64(params.Workers)*perWorker +
		checkpointBytes
	if total > p.limit {
		return nil, ErrBudgetExceeded
	}
	return &Budget{
		BytesLimit:        p.limit,
		PacketsPerSegment: params.PacketsPerSegment,
		Workers:           params.Workers,
		PerWorkerBytes:    perWorker,
		TotalBytes:        total,
	}, nil
}

// Choose searches decreasing segment sizes (keeping the requested worker
// count) for the first plan that fits. It never mutates disk state.
func (p *Planner) Choose(maxPacketsPerSegment, workers int) (*Budget, error) {
	if workers <= 0 {
		return nil, errors.New("plan: workers must be positive")
	}
	for n := maxPacketsPerSegment; n >= 1; n-- {
		if b, err := p.Evaluate(Params{PacketsPerSegment: n, Workers: workers}); err == nil {
			return b, nil
		}
	}
	return nil, ErrBudgetExceeded
}

// Headroom reports how many bytes remain under the limit for an accepted plan.
func (b *Budget) Headroom() int64 { return b.BytesLimit - b.TotalBytes }
