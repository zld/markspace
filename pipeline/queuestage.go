package pipeline

import (
	"context"
	"time"

	"mediapack/model"
	"mediapack/report"
	"mediapack/sample"
	"mediapack/stream"
)

// passThroughQueue moves all generated AV units through one fixed-capacity
// queue and records queue dwell latency. The queue occupancy can never exceed
// cfg.QueueCapacity.
func passThroughQueue(s *sample.Sample, cfg Config, collector *report.Collector) (int, error) {
	q, err := stream.NewBoundedQueue(cfg.QueueCapacity, stream.DefaultMaxUnitBytes)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			u, err := q.Get(ctx)
			if err != nil || u == nil {
				return
			}
			collector.ObserveLatency(u.Latency(time.Now()))
		}
	}()

	putAll := func(units []*model.Unit) error {
		for _, u := range units {
			if err := q.Put(ctx, u); err != nil {
				return err
			}
		}
		return nil
	}
	for _, repUnits := range s.Video {
		if err := putAll(repUnits); err != nil {
			cancel()
			q.Close()
			<-done
			return q.Peak(), err
		}
	}
	if err := putAll(s.Audio); err != nil {
		cancel()
		q.Close()
		<-done
		return q.Peak(), err
	}
	q.Close()
	<-done
	stats := q.Stats()
	collector.SetQueue(stats.Capacity, stats.PeakOccupancy)
	return stats.PeakOccupancy, nil
}
