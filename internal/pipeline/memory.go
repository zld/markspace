package pipeline

import (
	"runtime"
	"time"

	"mediascan/internal/stream"
)

// MemMonitor samples runtime heap peak while the stream Budget provides an
// exact accounted-buffer peak.
type MemMonitor struct {
	budget   *stream.Budget
	peakHeap uint64
	stop     chan struct{}
	done     chan struct{}
}

// StartMemMonitor begins sampling every 2ms and takes an immediate sample.
func StartMemMonitor(budget *stream.Budget) *MemMonitor {
	m := &MemMonitor{budget: budget, stop: make(chan struct{}), done: make(chan struct{})}
	m.sample()
	go func() {
		t := time.NewTicker(2 * time.Millisecond)
		defer t.Stop()
		defer close(m.done)
		for {
			select {
			case <-m.stop:
				m.sample()
				return
			case <-t.C:
				m.sample()
			}
		}
	}()
	return m
}

func (m *MemMonitor) sample() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if ms.HeapAlloc > m.peakHeap {
		m.peakHeap = ms.HeapAlloc
	}
}

// Stop ends sampling and returns the reportable peak in bytes: the larger of
// sampled Go heap and exact budget-accounted live buffers.
func (m *MemMonitor) Stop() int64 {
	close(m.stop)
	<-m.done
	m.sample()
	peak := int64(m.peakHeap)
	if bp := m.budget.Peak(); bp > peak {
		peak = bp
	}
	return peak
}
