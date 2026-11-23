package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"mediamux/media"
)

// gatedReader blocks every Read until released, exercising cancellation of a
// blocked read.
type gatedReader struct {
	gate   chan struct{}
	once   sync.Once
	closed chan struct{}
}

func newGatedReader() *gatedReader {
	return &gatedReader{gate: make(chan struct{}), closed: make(chan struct{})}
}

func (g *gatedReader) Read(p []byte) (int, error) {
	select {
	case <-g.gate:
		return copy(p, []byte("payload")), nil
	case <-g.closed:
		return 0, io.ErrClosedPipe
	}
}

func (g *gatedReader) Close() error {
	g.once.Do(func() { close(g.closed) })
	return nil
}

func TestChunkReaderBoundsMemory(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 10000)
	cr, err := NewChunkReader(context.Background(), io.NopCloser(bytes.NewReader(data)), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if cr.ChunkSize() > 1024 || cr.AllocatedBytes() > 1024 {
		t.Fatalf("chunk=%d allocated=%d exceeds limit", cr.ChunkSize(), cr.AllocatedBytes())
	}
	var got bytes.Buffer
	for {
		chunk, err := cr.NextChunk()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(chunk) > 1024 {
			t.Fatalf("chunk length %d exceeds limit", len(chunk))
		}
		got.Write(chunk)
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatal("chunked read changed the payload")
	}
	if cr.BytesRead() != int64(len(data)) {
		t.Fatalf("bytes read=%d", cr.BytesRead())
	}
}

func TestChunkReaderCancellationClosesBlockedRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := newGatedReader()
	cr, err := NewChunkReader(ctx, src, 512)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := cr.NextChunk()
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
		if !media.HasCode(err, media.CodeCanceled) {
			t.Fatalf("unexpected error %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked read was not released by cancellation")
	}
	select {
	case <-src.closed:
	default:
		t.Fatal("cancellation did not close the source")
	}
}

func TestBufferedRejectsOverflow(t *testing.T) {
	buf := make([]byte, 8)
	n, err := Buffered(context.Background(), bytes.NewReader([]byte("0123456789")), buf)
	if !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("err=%v n=%d", err, n)
	}
	n, err = Buffered(context.Background(), bytes.NewReader([]byte("12345678")), buf)
	if err != nil || n != 8 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestQueueBackpressureBlocksProducer(t *testing.T) {
	q, err := NewQueue[int](2, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := q.TryPush(1); err != nil {
		t.Fatal(err)
	}
	if err := q.TryPush(2); err != nil {
		t.Fatal(err)
	}
	if err := q.TryPush(3); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
	pushed := make(chan struct{})
	go func() {
		_ = q.Push(ctx, 3)
		close(pushed)
	}()
	select {
	case <-pushed:
		t.Fatal("producer must block while the queue is full")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := q.Pop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pushed:
	case <-time.After(time.Second):
		t.Fatal("producer did not resume after a consumer popped")
	}
	stats := q.Stats()
	if stats.Enqueued != 3 || stats.Dequeued != 1 || stats.BlockedNanos <= 0 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestQueueSlowConsumerStopsBranch(t *testing.T) {
	// A tiny queue plus a producer timeout: with nobody consuming, the
	// producer must stop the branch instead of blocking forever.
	q, err := NewQueue[int](1, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := q.Push(ctx, 1); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = q.Push(ctx, 2)
	if !IsSlowConsumer(err) {
		t.Fatalf("want slow consumer, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("producer returned before the timeout: %s", elapsed)
	}
	if q.Stats().TimedOut != 1 {
		t.Fatalf("stats=%+v", q.Stats())
	}
	// The queue is closed: further pushes fail and the consumer drains what
	// was committed before the stop.
	if err := q.TryPush(3); err == nil {
		t.Fatal("push after slow-consumer stop must fail")
	}
	if v, err := q.Pop(ctx); err != nil || v != 1 {
		t.Fatalf("v=%d err=%v", v, err)
	}
	if _, err := q.Pop(ctx); !IsDrained(err) {
		t.Fatalf("want drained, got %v", err)
	}
}

func TestQueueDrainsThenReportsClosed(t *testing.T) {
	q, err := NewQueue[string](4, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, v := range []string{"a", "b"} {
		if err := q.Push(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	_ = q.Close()
	for _, want := range []string{"a", "b"} {
		got, err := q.Pop(ctx)
		if err != nil || got != want {
			t.Fatalf("got %q err=%v", got, err)
		}
	}
	if _, err := q.Pop(ctx); !IsDrained(err) {
		t.Fatalf("want drained, got %v", err)
	}
	_ = q.Close() // idempotent
}

func TestQueueCancellationUnblocks(t *testing.T) {
	q, err := NewQueue[int](1, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := q.Pop(ctx)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !media.HasCode(err, media.CodeCanceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock consumer")
	}
}
