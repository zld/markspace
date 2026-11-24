package pipeline

import (
	"context"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"8MiB":  8 << 20,
		"64KiB": 64 << 10,
		"1MB":   1_000_000,
		"4096":  4096,
	}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Fatalf("ParseSize(%q)=%d,%v want %d", in, got, err, want)
		}
	}
}

func TestStreamAppliesMemoryBackpressure(t *testing.T) {
	ctx := context.Background()
	s := NewStream[string](Config{QueueCapacity: 10, MaxMemoryBytes: 10})
	if err := s.Push(ctx, "a", 6); err != nil {
		t.Fatal(err)
	}
	if err := s.Push(ctx, "b", 4); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Push(ctx, "c", 5) }()
	select {
	case err := <-done:
		t.Fatalf("push should be blocked by memory budget, got err=%v", err)
	case <-time.After(25 * time.Millisecond):
	}

	env := <-s.Items()
	if env.Value != "a" {
		t.Fatalf("value=%q want a", env.Value)
	}
	env.Done() // releases 6 bytes; 4 used + 5 reservation now fits

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked producer did not resume after release")
	}

	used, peak, limit := s.Stats()
	if limit != 10 || peak != 10 || used != 9 {
		t.Fatalf("stats used=%d peak=%d limit=%d want used=9 peak=10 limit=10", used, peak, limit)
	}
	for range 2 {
		env := <-s.Items()
		env.Done()
	}
	used, peak, _ = s.Stats()
	if used != 0 || peak != 10 {
		t.Fatalf("final used=%d peak=%d want 0/10", used, peak)
	}
}

func TestBoundedQueuePausesFastProducer(t *testing.T) {
	ctx := context.Background()
	s := NewStream[int](Config{QueueCapacity: 1, MaxMemoryBytes: 1 << 20})
	if err := s.Push(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	returned := make(chan struct{})
	go func() {
		_ = s.Push(ctx, 2, 1)
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatal("second push should block on bounded queue")
	case <-time.After(25 * time.Millisecond):
	}
	env := <-s.Items()
	env.Done()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("producer did not resume after consumer")
	}
	env = <-s.Items()
	env.Done()
}

func TestGateRejectsOversizedItem(t *testing.T) {
	g := NewGate(8)
	if err := g.Acquire(context.Background(), 9); err == nil {
		t.Fatal("expected memory limit error")
	}
}
