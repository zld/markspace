package stream

import (
	"context"
	"errors"
	"mediarc/model"
	"sync/atomic"
	"testing"
	"time"
)

func TestPipelineBackpressureBlocksUpstream(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	slow := func(_ context.Context, in <-chan Item, out chan<- Item) error {
		for item := range in {
			<-release
			out <- item
		}
		return nil
	}
	p, err := NewPipeline(ctx, Config{BufferSize: 1}, slow)
	if err != nil {
		t.Fatal(err)
	}
	// Fill the input queue plus the stage's in-flight item, then prove Send
	// blocks rather than buffering without bound.
	if err := p.Send(ctx, items(1)[0]); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { _, out := p.Buffered(); return out == 0 })
	blocked := make(chan error, 1)
	go func() {
		for i := 0; i < 10; i++ {
			if err := p.Send(ctx, Item{Seq: uint64(100 + i)}); err != nil {
				blocked <- err
				return
			}
		}
		blocked <- nil
	}()
	select {
	case <-blocked:
		t.Fatal("Send returned while the downstream stage was blocked")
	case <-time.After(150 * time.Millisecond):
	}
	if buffered := len(p.input); buffered > p.Cap() {
		t.Fatalf("input buffered %d, cap %d", buffered, p.Cap())
	}
	close(release)
	p.CloseInput()
	if got, err := Drain(ctx, p); err != nil {
		t.Fatalf("drain: %v", err)
	} else if len(got) == 0 {
		t.Fatal("no items after release")
	}
}

func TestPipelineTrySendReportsFull(t *testing.T) {
	ctx := context.Background()
	hold := make(chan struct{})
	stage := func(_ context.Context, in <-chan Item, out chan<- Item) error {
		<-hold
		for range in {
		}
		return nil
	}
	p, err := NewPipeline(ctx, Config{BufferSize: 1}, stage)
	if err != nil {
		t.Fatal(err)
	}
	if !p.TrySend(items(1)[0]) {
		t.Fatal("first TrySend should succeed")
	}
	// The stage is not reading yet, so the queue is full.
	if p.TrySend(Item{Seq: 1}) {
		t.Fatal("second TrySend should report a full queue")
	}
	close(hold)
	p.CloseInput()
	_ = p.Wait()
}

func TestPipelineErrorPropagatesAndReleases(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	failing := func(_ context.Context, in <-chan Item, out chan<- Item) error {
		for range in {
		}
		return newError(model.CodeInvalidRTP, "synthetic failure").WithCause(boom)
	}
	var laterRan atomic.Bool
	later := func(ctx context.Context, in <-chan Item, out chan<- Item) error {
		laterRan.Store(true)
		for range in {
		}
		return nil
	}
	p, err := NewPipeline(ctx, Config{BufferSize: 2}, failing, later)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Send(ctx, items(1)[0]); err != nil {
		t.Fatal(err)
	}
	p.CloseInput()
	waitErr := p.Wait()
	if waitErr == nil {
		t.Fatal("expected an error")
	}
	if !model.IsCode(waitErr, model.CodeInvalidRTP) {
		t.Fatalf("error = %v", waitErr)
	}
	if !errors.Is(waitErr, boom) {
		t.Fatalf("cause lost: %v", waitErr)
	}
	if p.Err() == nil {
		t.Fatal("Err should expose the failure")
	}
	// Output must be closed so downstream readers cannot block forever.
	select {
	case _, ok := <-p.Output():
		if ok {
			// A buffered item may still be delivered; drain the rest.
			for range p.Output() {
			}
		}
	case <-time.After(time.Second):
		t.Fatal("output channel was not closed after failure")
	}
	_ = laterRan.Load()
}
