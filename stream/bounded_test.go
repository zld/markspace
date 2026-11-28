package stream

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPipelineMemoryIsBounded(t *testing.T) {
	ctx := context.Background()
	const cap = 3
	var peak atomic.Int64
	stage := func(_ context.Context, in <-chan Item, out chan<- Item) error {
		for item := range in {
			if n := int64(len(in)); n > peak.Load() {
				peak.Store(n)
			}
			out <- item
		}
		return nil
	}
	p, err := NewPipeline(ctx, Config{BufferSize: cap}, stage)
	if err != nil {
		t.Fatal(err)
	}
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for _, item := range items(500) {
			item.Data = make([]byte, 32)
			if err := p.Send(ctx, item); err != nil {
				return
			}
		}
		p.CloseInput()
	}()
	got, err := Drain(ctx, p)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	<-producerDone
	if len(got) != 500 {
		t.Fatalf("got %d items", len(got))
	}
	if peak.Load() > cap {
		t.Fatalf("stage observed %d queued items, cap %d", peak.Load(), cap)
	}
}

func TestConcurrentProducers(t *testing.T) {
	ctx := context.Background()
	p, err := NewPipeline(ctx, Config{BufferSize: 4}, func(_ context.Context, in <-chan Item, out chan<- Item) error {
		for item := range in {
			out <- item
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for producer := 0; producer < 4; producer++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if err := p.Send(ctx, Item{Seq: uint64(base*25 + i)}); err != nil {
					return
				}
			}
		}(producer)
	}
	go func() {
		wg.Wait()
		p.CloseInput()
	}()
	got, err := Drain(ctx, p)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("got %d items, want 100", len(got))
	}
	seen := map[uint64]bool{}
	for _, item := range got {
		if seen[item.Seq] {
			t.Fatalf("duplicate seq %d", item.Seq)
		}
		seen[item.Seq] = true
	}
}
