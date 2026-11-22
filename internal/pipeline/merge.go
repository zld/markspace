package pipeline

import (
	"context"
	"errors"
	"io"
	"sort"
)

// orderedLess defines the total ordering of results. Primary key is the
// shared-base timestamp; ties resolve by track number, then sequence number
// and finally the global ingestion index, which makes the order total even
// when two samples share every other field. A total order is what makes
// reruns byte-identical rather than merely "mostly sorted".
func orderedLess(a, b Result) bool {
	switch {
	case a.Task.NS != b.Task.NS:
		return a.Task.NS < b.Task.NS
	case a.Task.Track != b.Task.Track:
		return a.Task.Track < b.Task.Track
	case a.Task.Seq != b.Task.Seq:
		return a.Task.Seq < b.Task.Seq
	default:
		return a.Task.Index < b.Task.Index
	}
}

// SortResults returns results in the deterministic merge order. It does not
// allocate new Result values, only reorders the slice.
func SortResults(in []Result) []Result {
	sort.SliceStable(in, func(i, j int) bool { return orderedLess(in[i], in[j]) })
	return in
}

// Source yields tasks until io.EOF. Implementations must honor ctx: a
// cancelled context should return promptly so cancellation reaches the pool.
type Source func(ctx context.Context) (Task, error)

// Sink consumes one merged result. Returning an error cancels the whole
// pipeline; the sink is responsible for not publishing partial output.
type Sink func(ctx context.Context, r Result) error

// Run drives the full stage: pull from source with back-pressure, process on
// the bounded pool, merge deterministically and feed the sink. Cancellation
// from any component (ctx, source error, worker error, sink error) propagates
// to every other component and Run returns after the pool has fully stopped,
// guaranteeing no worker outlives the call and no half-finished output is
// published.
func Run(ctx context.Context, workers, queueDepth int,
	source Source, process func(context.Context, Task) Result, sink Sink) ([]Result, error) {

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pool, err := NewPool(runCtx, workers, queueDepth, process)
	if err != nil {
		return nil, err
	}

	srcErr := feed(runCtx, pool, source)
	// feed has stopped touching the input queue. Cancel so blocked workers
	// unblock on failure; on a clean EOF runCtx is still live and Close takes
	// the graceful drain path instead.
	if srcErr != nil {
		cancel()
	}
	pool.Close()
	drained := pool.Collected()
	if srcErr != nil && !errors.Is(srcErr, context.Canceled) {
		return SortResults(drained), srcErr
	}

	// Worker errors fail the run before the sink sees anything, so a damaged
	// intermediate result can never be published as a finished package.
	for _, r := range drained {
		if r.Err != nil {
			cancel()
			return SortResults(drained), r.Err
		}
	}

	merged := SortResults(drained)
	for _, r := range merged {
		if err := sink(runCtx, r); err != nil {
			cancel()
			return merged, err
		}
	}
	if err := runCtx.Err(); err != nil && ctx.Err() != nil {
		return merged, ctx.Err()
	}
	return merged, nil
}

// feed pulls every source task and submits it, translating io.EOF into a
// clean stop. A cancelled context unblocks Submit and surfaces ctx.Err.
func feed(ctx context.Context, pool *Pool, source Source) error {
	for {
		task, err := source(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := pool.Submit(task); err != nil {
			return err
		}
	}
}
