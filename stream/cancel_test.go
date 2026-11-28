package stream

import (
	"context"
	"testing"
	"time"

	"mediarc/model"
)

func TestPipelineCancellationUnblocksSenders(t *testing.T) {
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
		t.Fatal("seed failed")
	}
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- p.Send(ctx, Item{Seq: 99})
	}()
	waitFor(t, time.Second, func() bool { return p.TrySend(Item{Seq: 100}) == false })
	// Cancel while the stage is still parked: the queue stays full, so the only
	// way for the blocked sender to return is the cancellation path.
	p.Cancel()
	select {
	case err := <-sendErr:
		if err == nil {
			t.Fatal("send after cancel should fail")
		}
		if !model.IsCode(err, model.CodeCanceled) {
			t.Fatalf("send error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked sender was not released by cancellation")
	}
	close(hold)
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		if !model.IsCode(err, model.CodeCanceled) {
			t.Fatalf("wait error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after cancellation")
	}
}

func TestPipelineContextCancellation(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	p, err := NewPipeline(parent, Config{BufferSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	parentCancel()
	select {
	case err := <-func() chan error {
		ch := make(chan error, 1)
		go func() { ch <- p.Wait() }()
		return ch
	}():
		if !model.IsCode(err, model.CodeCanceled) {
			t.Fatalf("wait error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait hung after parent cancellation")
	}
}

func TestPipelineConfigValidation(t *testing.T) {
	if _, err := NewPipeline(context.Background(), Config{BufferSize: 0}); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatalf("zero buffer: %v", err)
	}
	if _, err := NewPipeline(context.Background(), Config{BufferSize: -3}); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatalf("negative buffer: %v", err)
	}
}
