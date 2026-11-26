package stream

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// PipelineStats reports deterministic execution facts.
type PipelineStats struct {
	Produced  int  `json:"produced"`
	Consumed  int  `json:"consumed"`
	QueuePeak int  `json:"queue_peak"`
	QueueCap  int  `json:"queue_cap"`
	Cancelled bool `json:"cancelled"`
}

// Pipeline wires a producer to one slow-capable consumer through Queue.
type Pipeline struct {
	Queue   *Queue
	Produce func(ctx context.Context, emit func(Block) error) error
	Consume func(ctx context.Context, b Block) error
	Staging *Staging
}

// Run executes producer and consumer. Consumer failure cancels production and
// aborts unpublished staging. A successful run leaves commit to the publisher.
func (p *Pipeline) Run(parent context.Context) (PipelineStats, error) {
	if p.Queue == nil || p.Produce == nil || p.Consume == nil {
		return PipelineStats{}, fmt.Errorf("pipeline requires queue, producer and consumer")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stats := PipelineStats{QueueCap: p.Queue.Cap()}
	var mu sync.Mutex
	var producerErr, consumerErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer p.Queue.Close()
		err := p.Produce(ctx, func(b Block) error {
			if err := p.Queue.Push(ctx, b); err != nil {
				return err
			}
			mu.Lock()
			stats.Produced++
			mu.Unlock()
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			producerErr = err
		}
	}()

	go func() {
		defer wg.Done()
		for {
			b, ok, err := p.Queue.Pop(ctx)
			if err != nil {
				consumerErr = err
				return
			}
			if !ok {
				return
			}
			if err := p.Consume(ctx, b); err != nil {
				consumerErr = fmt.Errorf("%w block=%d: %v", ErrConsumer, b.Index, err)
				cancel()
				return
			}
			mu.Lock()
			stats.Consumed++
			mu.Unlock()
		}
	}()

	wg.Wait()
	stats.QueuePeak = p.Queue.Peak()
	stats.Cancelled = parent.Err() != nil || (consumerErr == nil && ctx.Err() != nil)
	if consumerErr != nil || producerErr != nil || parent.Err() != nil {
		if p.Staging != nil {
			_ = p.Staging.Abort()
		}
		switch {
		case consumerErr != nil:
			return stats, consumerErr
		case producerErr != nil:
			return stats, producerErr
		default:
			return stats, parent.Err()
		}
	}
	return stats, nil
}

// SlowConsume sleeps per block but remains cancellation-aware.
func SlowConsume(d time.Duration, fn func(Block) error) func(context.Context, Block) error {
	return func(ctx context.Context, b Block) error {
		if d > 0 {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
			}
		}
		return fn(b)
	}
}
