// Package worker runs processing tasks with bounded concurrency.
package worker

import (
	"context"
	"sync"
)

// Task is one unit of work.
type Task struct {
	ID  string
	Run func(ctx context.Context) error
}

// Pool executes tasks with at most Size running concurrently.
type Pool struct{ Size int }

// Run executes all tasks, cancelling the rest on the first error.
func (p *Pool) Run(ctx context.Context, tasks []Task) error {
	if p.Size < 1 {
		p.Size = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, p.Size)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error

	for _, t := range tasks {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			if err := t.Run(ctx); err != nil {
				once.Do(func() {
					firstErr = err
					cancel()
				})
			}
		}()
	}
	wg.Wait()
	return firstErr
}
