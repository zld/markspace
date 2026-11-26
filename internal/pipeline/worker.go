package pipeline

import (
	"context"
	"sort"
	"sync"
)

// ErrStoppedAfter is returned by OrderedPool when the caller's yield
// callback requests a resumable stop after a configured number of results.
var ErrStoppedAfter = stopError{}

type stopError struct{}

func (stopError) Error() string { return "stopped after requested checkpoint" }

// Task is an indexed unit of work. Index determines chronological result
// order regardless of worker completion order.
type Task[T any] struct {
	Index int
	Run   func(context.Context) (T, error)
}

// OrderedPool runs tasks with a fixed worker count and invokes yield in
// ascending Index order. If yield returns an error, the pool cancels
// outstanding work and returns that error after workers exit.
func OrderedPool[T any](ctx context.Context, workers int, tasks []Task[T], yield func(index int, value T) error) error {
	if workers <= 0 {
		workers = 1
	}
	if workers > len(tasks) && len(tasks) > 0 {
		workers = len(tasks)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobCh := make(chan Task[T])
	resultCh := make(chan result[T], workers+1)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobCh {
				if ctx.Err() != nil {
					return
				}
				v, err := task.Run(ctx)
				select {
				case resultCh <- result[T]{index: task.Index, value: v, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
	feed:
		for _, task := range tasks {
			select {
			case jobCh <- task:
			case <-ctx.Done():
				break feed
			}
		}
		close(jobCh)
		wg.Wait()
		close(resultCh)
	}()

	values := make(map[int]result[T])
	next := 0
	var yieldErr error
	for res := range resultCh {
		if res.err != nil {
			cancel()
			// Drain until channel closes; preserve first execution error.
			if yieldErr == nil {
				yieldErr = res.err
			}
			continue
		}
		values[res.index] = res
		for {
			r, ok := values[next]
			if !ok {
				break
			}
			delete(values, next)
			if yieldErr == nil {
				if err := yield(next, r.value); err != nil {
					yieldErr = err
					cancel()
				}
			}
			next++
		}
	}
	if yieldErr != nil {
		return yieldErr
	}
	// Defensive ordered flush if a cancellation/error path left contiguous
	// values; normal successful execution has already flushed them.
	indices := make([]int, 0, len(values))
	for i := range values {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	for _, i := range indices {
		if i != next {
			break
		}
		if err := yield(i, values[i].value); err != nil {
			return err
		}
		next++
	}
	return nil
}

type result[T any] struct {
	index int
	value T
	err   error
}
