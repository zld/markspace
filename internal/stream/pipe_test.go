package stream

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"mediad/internal/fault"
)

func TestFIFOAndCloseDrain(t *testing.T) {
	p := NewPipe[int](3)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if err := p.Push(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		v, err := p.Pop(ctx)
		if err != nil || v != i {
			t.Fatalf("Pop = %d, %v want %d", v, err, i)
		}
	}
	p.Close()
	if _, err := p.Pop(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("drained closed pipe must return EOF, got %v", err)
	}
}

func TestSlowConsumerBackpressureBoundedPeak(t *testing.T) {
	const cap = 4
	p := NewPipe[int](cap)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if err := p.Push(ctx, i); err != nil {
				return
			}
		}
		p.Close()
	}()

	consumed := 0
	for {
		v, err := p.Pop(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if v != consumed {
			t.Fatalf("order broken: %d != %d", v, consumed)
		}
		consumed++
		time.Sleep(time.Millisecond) // deliberately slow consumer
	}
	wg.Wait()
	if consumed != 200 {
		t.Fatalf("consumed %d chunks", consumed)
	}
	if peak := p.Snapshot().PeakQueued; peak > cap {
		t.Fatalf("peak queue %d exceeded capacity %d", peak, cap)
	}
}

func TestCancellationReleasesBlockedPushAndPop(t *testing.T) {
	p := NewPipe[int](1)
	ctx, cancel := context.WithCancel(context.Background())

	// Fill the only slot so the next Push blocks.
	if err := p.Push(ctx, 1); err != nil {
		t.Fatal(err)
	}
	pushErr := make(chan error, 1)
	go func() { pushErr <- p.Push(ctx, 2) }()

	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-pushErr:
		if fe, ok := fault.As(err); !ok || fe.Category != fault.CatCanceled {
			t.Fatalf("blocked push got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not release blocked push")
	}

	// A blocked Pop on an empty pipe must also return on cancellation.
	empty := NewPipe[int](1)
	ctx2, cancel2 := context.WithCancel(context.Background())
	popErr := make(chan error, 1)
	go func() { _, err := empty.Pop(ctx2); popErr <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel2()
	select {
	case err := <-popErr:
		if fe, ok := fault.As(err); !ok || fe.Category != fault.CatCanceled {
			t.Fatalf("blocked pop got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not release blocked pop")
	}
}

func TestCloseWithErrorAbortsWaiters(t *testing.T) {
	p := NewPipe[string](2)
	ctx := context.Background()
	errBoom := errors.New("disk failure")
	go func() {
		time.Sleep(20 * time.Millisecond)
		p.CloseWithError(errBoom)
	}()
	if _, err := p.Pop(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("Pop error = %v, want wrapped boom", err)
	}
	if err := p.Push(ctx, "x"); err == nil {
		t.Fatal("push after abort must fail")
	}
}
