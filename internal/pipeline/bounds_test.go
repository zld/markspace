package pipeline

import "testing"

func TestPoolQueueHardCap(t *testing.T) {
	// A caller requesting an unbounded queue is clamped to the hard cap so
	// memory stays bounded by MaxQueue regardless of configuration.
	p := NewPool(PoolConfig{Workers: 1, Queue: 1 << 20})
	if p.Config().Queue != MaxQueue {
		t.Fatalf("queue clamp=%d want %d", p.Config().Queue, MaxQueue)
	}
}

func TestPoolWorkerHardCap(t *testing.T) {
	p := NewPool(PoolConfig{Workers: 1 << 20, Queue: 1})
	if p.Config().Workers != MaxWorkers {
		t.Fatalf("worker clamp=%d want %d", p.Config().Workers, MaxWorkers)
	}
}

func TestPoolZeroDefaultsToOne(t *testing.T) {
	p := NewPool(PoolConfig{})
	if p.Config().Workers != 1 || p.Config().Queue != 1 {
		t.Fatalf("zero config not normalized: %+v", p.Config())
	}
}
