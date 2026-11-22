package stream

import (
	"context"
	"io"
	"time"

	"mediaqc/internal/timeline"
)

// RunnerConfig controls queue capacity and simulated consumer cost.
type RunnerConfig struct {
	QueueCap      int
	ConsumerDelay time.Duration // models a slow downstream
}

// RunStats summarises one execution for the metrics stage.
type RunStats struct {
	Packets         int
	PeakQueueLen    int
	PeakBufferBytes int64
	Latencies       []time.Duration // per-packet enqueue->consumed
}

// ConsumeFunc processes one packet (e.g. writes it to the remuxer).
type ConsumeFunc func(p Packet) error

// Run streams packets (already located via the index) from r through a
// bounded queue to consume. Media payloads are read one block at a
// time; nothing but queue capacity is held in memory. On cancellation
// the queue is closed, the producer stops, and a ClassInterrupted fault
// is returned so the caller can clean up and exit 3.
func Run(ctx context.Context, r io.ReaderAt, packets []Packet, cfg RunnerConfig, consume ConsumeFunc) (*RunStats, error) {
	q := NewQueue(cfg.QueueCap)
	stats := &RunStats{}
	prodErr := make(chan error, 1)

	go func() {
		defer q.Close()
		for i := range packets {
			p := packets[i]
			if p.Data == nil && p.ReadLen > 0 {
				data := make([]byte, p.ReadLen)
				if _, err := r.ReadAt(data, p.ReadOff); err != nil {
					prodErr <- timeline.NewFault(timeline.ClassTruncated, p.ReadOff, p.Track, "%v", err)
					return
				}
				p.Data = data
			}
			if !q.Push(ctx, p) {
				prodErr <- ctx.Err()
				return
			}
		}
		prodErr <- nil
	}()

	for {
		p, ok := q.Pop(ctx)
		if !ok {
			if ctx.Err() != nil {
				return stats, timeline.NewFault(timeline.ClassInterrupted, p.Offset, p.Track,
					"cancelled after %d packets", stats.Packets)
			}
			break
		}
		if cfg.ConsumerDelay > 0 {
			select {
			case <-time.After(cfg.ConsumerDelay):
			case <-ctx.Done():
				return stats, timeline.NewFault(timeline.ClassInterrupted, p.Offset, p.Track,
					"cancelled after %d packets", stats.Packets)
			}
		}
		if err := consume(p); err != nil {
			return stats, err
		}
		stats.Latencies = append(stats.Latencies, time.Since(p.Enqueued))
		stats.Packets++
	}
	if err := <-prodErr; err != nil {
		if ctx.Err() != nil {
			return stats, timeline.NewFault(timeline.ClassInterrupted, -1, 0, "%v", ctx.Err())
		}
		return stats, err
	}
	stats.PeakQueueLen, stats.PeakBufferBytes = q.Stats()
	return stats, nil
}
