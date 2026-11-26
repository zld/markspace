package flow

import (
	"context"
	"errors"
	"fmt"
)

type configError struct{ msg string }

func (e *configError) Error() string { return "flow config: " + e.msg }

func errConfig(msg string) error { return &configError{msg: msg} }

// Merger accepts results in completion order and releases only a contiguous
// sequence prefix. Out-of-order results are buffered until their predecessors
// arrive; this is what makes checkpointing safe, because the last released
// sequence is always a boundary that can be resumed from.
type Merger struct {
	next int
	buf  map[int]Result
	// released records the merged results in sequence order for auditing.
	released []Result
	lastSeq  int
	started  bool
}

// NewMerger starts at sequence 1.
func NewMerger() *Merger {
	return &Merger{next: 1, buf: make(map[int]Result)}
}

// Add buffers a result and returns, in order, every result that became
// releasable. The error of a released result does not stop later releases in
// the same call; the caller decides whether to abort.
func (m *Merger) Add(r Result) []Result {
	if r.Seq < m.next {
		return nil
	}
	m.buf[r.Seq] = r
	var out []Result
	for {
		next, ok := m.buf[m.next]
		if !ok {
			break
		}
		delete(m.buf, m.next)
		out = append(out, next)
		m.released = append(m.released, next)
		m.next++
		m.lastSeq = next.Seq
		m.started = true
	}
	return out
}

// LastSeq is the highest contiguous sequence released so far, or 0 when
// nothing has been released.
func (m *Merger) LastSeq() int { return m.lastSeq }

// NextSeq is the sequence the merger is waiting for.
func (m *Merger) NextSeq() int { return m.next }

// Pending counts buffered out-of-order results.
func (m *Merger) Pending() int { return len(m.buf) }

// Released returns the released results in order.
func (m *Merger) Released() []Result { return append([]Result(nil), m.released...) }

// DrainGap reports the sequence numbers still missing when the producer ends.
// A non-empty gap means the run cannot be checkpointed past LastSeq.
func (m *Merger) DrainGap() []int {
	if len(m.buf) == 0 {
		return nil
	}
	out := make([]int, 0, len(m.buf))
	for seq := range m.buf {
		out = append(out, seq)
	}
	sortInts(out)
	return out
}

func sortInts(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// ErrGap reports missing sequence numbers at the end of a run.
func (m *Merger) ErrGap() error {
	gap := m.DrainGap()
	if len(gap) == 0 {
		return nil
	}
	return fmt.Errorf("merger: %d result(s) never completed, first missing sequence %d", len(gap), gap[0])
}

// PipelineError distinguishes cancellation from work failures for exit-code
// selection.
type PipelineError struct {
	Canceled bool
	Err      error
}

func (e *PipelineError) Error() string {
	if e.Canceled {
		return "canceled: " + e.Err.Error()
	}
	return e.Err.Error()
}

func (e *PipelineError) Unwrap() error { return e.Err }

// IsCanceled reports whether err is context cancellation or a pipeline that
// propagated one.
func IsCanceled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var pe *PipelineError
	if errors.As(err, &pe) {
		return pe.Canceled
	}
	return false
}
