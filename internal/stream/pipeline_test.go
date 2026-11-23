package stream

import (
	"context"
	"errors"
	"testing"
	"time"

	"mediqc/internal/fault"
)

func TestBackpressureBoundsMemory(t *testing.T) {
	const max = 1024
	p := New(max)
	ctx := context.Background()
	sink := &SlowSink{Delay: 2 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- Drain(ctx, p, sink) }()
	chunk := Chunk{Payload: make([]byte, 256)}
	for i := 0; i < 40; i++ {
		chunk.Seq = i
		if err := p.Push(ctx, chunk); err != nil {
			t.Fatal(err)
		}
	}
	p.Close(nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if sink.Chunks != 40 {
		t.Fatalf("sink got %d chunks, want 40 (no drops)", sink.Chunks)
	}
	if p.PeakBytes > max {
		t.Fatalf("peak %d exceeds limit %d", p.PeakBytes, max)
	}
	if p.PeakBytes == 0 {
		t.Fatal("peak should record buffering")
	}
}

func TestCancelStopsProducer(t *testing.T) {
	p := New(256) // one chunk fills the buffer
	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Push(ctx, Chunk{Payload: make([]byte, 256)}); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- p.Push(ctx, Chunk{Payload: make([]byte, 256)}) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-errc; !fault.IsCode(err, fault.CodeCancelled) {
		t.Fatalf("want E_CANCELLED, got %v", err)
	}
}

func TestSinkFailureClosesQueue(t *testing.T) {
	p := New(1 << 20)
	boom := errors.New("disk full")
	sink := SinkFunc(func(c Chunk) error { return boom })
	done := make(chan error, 1)
	go func() { done <- Drain(context.Background(), p, sink) }()
	if err := p.Push(context.Background(), Chunk{Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, boom) {
		t.Fatalf("drain err %v", err)
	}
	// Queue is closed: further pushes fail with E_SINK_CLOSED.
	if err := p.Push(context.Background(), Chunk{Payload: []byte("y")}); !fault.IsCode(err, fault.CodeSinkClosed) {
		t.Fatalf("want E_SINK_CLOSED, got %v", err)
	}
}

func TestOversizeChunkRejected(t *testing.T) {
	p := New(16)
	if err := p.Push(context.Background(), Chunk{Payload: make([]byte, 32)}); !fault.IsCode(err, fault.CodeFrameOverflow) {
		t.Fatalf("want E_FRAME_OVERFLOW, got %v", err)
	}
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(Chunk) error

func (f SinkFunc) Write(c Chunk) error { return f(c) }
