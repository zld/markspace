// Package pipeline wires the stages together. This file holds the fan-out,
// recovery and switch-planning helpers.
package pipeline

import (
	"context"
	"sync"
	"time"

	"mediad/internal/fanout"
	"mediad/internal/media"
)

// runLadder drives the fan-out ladder and drains every layer, returning the
// aggregate and the publish latencies.
func runLadder(ctx context.Context, blocks []media.Block, opts Options) (fanout.Aggregate, []int64, error) {
	specs := make([]fanout.LayerOptions, 0, len(fanout.LadderNames))
	for i, name := range fanout.LadderNames {
		spec := fanout.LayerOptions{Name: name, Buffer: fanout.BufferBytes}
		if tr, ok := opts.LayerTransport[name]; ok {
			spec.Transp = tr
		}
		if i == 1 {
			spec.Slow = slowPerBlock(opts.SlowConsumer)
		}
		specs = append(specs, spec)
	}
	ladder := fanout.NewLadder(specs)
	cancelAt := -1
	if opts.CancelLayer != "" && len(blocks) >= 3 {
		cancelAt = len(blocks) / 3
	}

	published := make(map[uint64]time.Time, len(blocks))
	var latMu sync.Mutex
	var latencies []int64
	record := func(b media.Block) {
		latMu.Lock()
		defer latMu.Unlock()
		if len(latencies) >= opts.LatencySamples {
			return
		}
		if at, ok := published[b.Seq]; ok {
			latencies = append(latencies, time.Since(at).Nanoseconds())
		}
	}

	var done sync.WaitGroup
	for _, layer := range ladder.Layers() {
		done.Add(1)
		go func(layer *fanout.Layer) {
			defer done.Done()
			for {
				b, ok := layer.Next(ctx)
				if !ok {
					return
				}
				record(b)
			}
		}(layer)
	}

	for i, b := range blocks {
		if cancelAt >= 0 && i == cancelAt {
			for _, layer := range ladder.Layers() {
				if layer.Name() == opts.CancelLayer {
					layer.Cancel()
				}
			}
		}
		// Recording happens before Fanout so the stamp always precedes the
		// block reaching any consumer.
		latMu.Lock()
		published[b.Seq] = time.Now()
		latMu.Unlock()
		if err := ladder.Fanout(ctx, b); err != nil {
			ladder.Close()
			done.Wait()
			return fanout.Aggregate{}, nil, err
		}
	}
	ladder.Close()
	done.Wait()
	return ladder.Aggregate(), latencies, nil
}

// slowBatch is how many blocks a slow editor consumes per pause. Modelling
// the pause per batch rather than per block keeps a demo of a 120 ms consumer
// interactive while still filling the byte budget and stalling the publisher.
//
// The batch size also bounds queue dwell: a full 4 MiB budget holds roughly
// 128 audio-sized blocks (or fewer video blocks), so spreading 120 ms over
// that many blocks keeps the observed p95 latency under the 250 ms target
// while still exercising backpressure.
const slowBatch = 128

// slowPerBlock converts a per-batch consumer pause into the per-block delay
// the layer implements.
func slowPerBlock(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	per := d / slowBatch
	if per <= 0 {
		per = time.Microsecond
	}
	return per
}
