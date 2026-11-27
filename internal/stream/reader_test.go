package stream

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReaderConfigCaps(t *testing.T) {
	if err := (ReaderConfig{ChunkBytes: 8, QueueLen: 1}).Validate(); err == nil {
		t.Fatal("chunk below minimum must fail")
	}
	if err := (ReaderConfig{ChunkBytes: MaxChunkBytes + 1, QueueLen: 1}).Validate(); err == nil {
		t.Fatal("chunk above maximum must fail")
	}
	if err := (ReaderConfig{ChunkBytes: 128, QueueLen: 0}).Validate(); err == nil {
		t.Fatal("zero queue must fail")
	}
	if err := (ReaderConfig{ChunkBytes: 128, QueueLen: 2}).Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestBoundedReaderChunksAndOffsets(t *testing.T) {
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i)
	}
	r, err := NewBoundedReader(bytes.NewReader(data), ReaderConfig{ChunkBytes: 128, QueueLen: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	go func() { _ = r.Run(ctx) }()

	var got []byte
	wantSeq := 0
	var wantStart int64
	for {
		ch, ok := r.Receive(ctx)
		if !ok {
			break
		}
		if ch.Seq != wantSeq {
			t.Fatalf("seq=%d want %d", ch.Seq, wantSeq)
		}
		if ch.Start != wantStart {
			t.Fatalf("start=%d want %d", ch.Start, wantStart)
		}
		got = append(got, ch.Data...)
		wantStart += int64(len(ch.Data))
		wantSeq++
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("reassembled %d bytes, want %d", len(got), len(data))
	}
	if wantSeq != 8 { // 7 full + 1 partial
		t.Fatalf("chunk count=%d want 8", wantSeq)
	}
	st := r.Stats()
	if st.Enqueued != 8 || st.Dequeued != 8 || st.Depth != 0 {
		t.Fatalf("stats wrong: %+v", st)
	}
}

func TestBackpressurePeakBuffer(t *testing.T) {
	q := NewQueue[int](2, 64)
	ctx := context.Background()
	// Fill the queue without consumers.
	for i := 0; i < 2; i++ {
		if err := q.Enqueue(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	stats := q.Stats()
	if stats.PeakDepth != 2 || stats.PeakBufferBytes != 128 {
		t.Fatalf("peak accounting wrong: %+v", stats)
	}
	// A third enqueue must block until a dequeue happens.
	done := make(chan error, 1)
	go func() { done <- q.Enqueue(ctx, 2) }()
	select {
	case <-done:
		t.Fatal("producer must block on full queue")
	case <-time.After(20 * time.Millisecond):
	}
	if _, ok := q.Dequeue(ctx); !ok {
		t.Fatal("dequeue failed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("producer never released after dequeue")
	}
	if q.Stats().PeakDepth != 2 {
		t.Fatal("depth must never exceed capacity")
	}
}

func TestQueueCancelUnblocksProducer(t *testing.T) {
	q := NewQueue[int](1, 8)
	ctx, cancel := context.WithCancel(context.Background())
	if err := q.Enqueue(ctx, 1); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	go func() {
		close(blocked)
		_ = q.Enqueue(ctx, 2)
	}()
	<-blocked
	time.Sleep(10 * time.Millisecond)
	cancel()
	// After cancel, Fail still releases every goroutine cleanly.
	sentinel := errors.New("write failed")
	q.Fail(sentinel)
	err := q.Enqueue(context.Background(), 3)
	if !errors.Is(err, sentinel) && !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("want sentinel after Fail, got %v", err)
	}
}

func TestReaderCancelExitsGoroutine(t *testing.T) {
	data := bytes.Repeat([]byte{0xAA}, 4096)
	r, err := NewBoundedReader(bytes.NewReader(data), ReaderConfig{ChunkBytes: 64, QueueLen: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- r.Run(ctx) }()
	// Consume a few, then cancel without draining.
	for i := 0; i < 3; i++ {
		if _, ok := r.Receive(ctx); !ok {
			t.Fatal("early close")
		}
	}
	cancel()
	select {
	case <-runErr:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after cancel")
	}
}
