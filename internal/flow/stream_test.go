package flow

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBoundedQueueBlocksAndDrains(t *testing.T) {
	s := New(2)
	ctx := context.Background()
	if err := s.Send(ctx, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(ctx, []byte{2}); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Fatalf("len = %d, want 2", s.Len())
	}
	got, ok, err := s.Recv(ctx)
	if err != nil || !ok || got[0] != 1 {
		t.Fatalf("recv = %v ok=%v err=%v", got, ok, err)
	}
	// After one slot frees, a third send must succeed.
	if err := s.Send(ctx, []byte{3}); err != nil {
		t.Fatal(err)
	}
	s.CloseWithError(nil)
	for want := byte(2); ; want++ {
		item, ok, err := s.Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			if want != 4 {
				t.Fatalf("drained early at want=%d", want)
			}
			return
		}
		if item[0] != want {
			t.Fatalf("item = %d, want %d", item[0], want)
		}
	}
}

func TestSendBlocksOnFullQueueThenReleasedByCancel(t *testing.T) {
	s := New(1)
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Send(ctx, []byte("x")); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		released <- s.Send(ctx, []byte("blocked"))
	}()
	select {
	case err := <-released:
		t.Fatalf("producer should block on full queue, returned %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-released:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not release the blocked producer")
	}
}

func TestSendBlocksThenReleasedByWriterFailure(t *testing.T) {
	s := New(1)
	ctx := context.Background()
	fatal := errors.New("disk full")
	if err := s.Send(ctx, []byte("x")); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() { released <- s.Send(ctx, []byte("blocked")) }()
	time.Sleep(20 * time.Millisecond)
	s.CloseWithError(fatal)
	select {
	case err := <-released:
		if !errors.Is(err, fatal) {
			t.Fatalf("expected disk-full error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseWithError did not release the blocked producer")
	}
	if err := s.Send(ctx, []byte("z")); err == nil {
		t.Fatal("send after close must fail")
	}
}

func TestRecvCancellation(t *testing.T) {
	s := New(1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, _, err := s.Recv(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected Canceled, got %v", err)
	}
}
