package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReaderChunkingAndOffsets(t *testing.T) {
	src := bytes.Repeat([]byte("abcdefghij"), 13) // 130 bytes
	r, err := Open(bytes.NewReader(src), Options{ChunkBytes: 7, Queue: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var got []byte
	var offsets []int64
	for {
		ch, err := r.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		offsets = append(offsets, ch.Offset)
		got = append(got, ch.Data...)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("data mismatch: got %d bytes want %d", len(got), len(src))
	}
	for i, off := range offsets {
		if off != int64(i*7) {
			t.Fatalf("chunk %d offset=%d", i, off)
		}
	}
	if st := r.Stats(); st.Chunks != 19 || st.Bytes != 130 {
		t.Fatalf("stats=%+v", st)
	}
}

func TestReaderCancellationReleasesProducer(t *testing.T) {
	pr, pw := io.Pipe()
	r, err := Open(pr, Options{ChunkBytes: 4, Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = pw.Write([]byte("first")) }()
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := r.Next(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = r.Next(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = pw.CloseWithError(io.ErrClosedPipe)
		_ = r.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not shut down after cancellation")
	}
}

// slowConsumer blocks on a gate, letting the test observe that the producer
// stalls instead of buffering unboundedly.
type slowConsumer struct {
	name    string
	gate    chan struct{}
	delay   time.Duration
	mu      sync.Mutex
	got     [][]byte
	closed  int
	peakLag int64
}

func (s *slowConsumer) Name() string { return s.name }

func (s *slowConsumer) Offer(ctx context.Context, ch Chunk) error {
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	s.got = append(s.got, append([]byte(nil), ch.Data...))
	s.mu.Unlock()
	return nil
}

func (s *slowConsumer) Close() error {
	s.mu.Lock()
	s.closed++
	s.mu.Unlock()
	return nil
}

func (s *slowConsumer) chunks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

func feedOf(data []byte, chunk int, layer string) Feed {
	off := 0
	return FeedFunc(func(ctx context.Context) (Item, error) {
		if off >= len(data) {
			return Item{}, io.EOF
		}
		end := off + chunk
		if end > len(data) {
			end = len(data)
		}
		item := Item{Layer: layer, Chunk: Chunk{Offset: int64(off), Data: data[off:end]}}
		off = end
		return item, nil
	})
}

// TestFanoutBackpressurePeakBounded proves that a stalled consumer stops the
// producer and peak buffering never exceeds queue+1 in-flight chunks.
func TestFanoutBackpressurePeakBounded(t *testing.T) {
	const queue = 2
	data := bytes.Repeat([]byte("x"), 64)
	fast := &slowConsumer{name: "fast"}
	blocked := &slowConsumer{name: "slow", gate: make(chan struct{})}

	// Route each chunk to both layers, interleaved deterministically.
	off := 0
	feed := FeedFunc(func(ctx context.Context) (Item, error) {
		if off >= len(data) {
			return Item{}, io.EOF
		}
		layer := "fast"
		if off/8%2 == 0 {
			layer = "slow"
		}
		end := off + 8
		if end > len(data) {
			end = len(data)
		}
		item := Item{Layer: layer, Chunk: Chunk{Offset: int64(off), Data: data[off:end]}}
		off = end
		return item, nil
	})

	f, err := NewFanout(context.Background(), feed, queue, fast, blocked)
	if err != nil {
		t.Fatal(err)
	}

	// Let the slow lane fill its queue and stall the producer.
	time.Sleep(100 * time.Millisecond)
	depth := len(f.consumers["slow"].queue)
	if depth > queue {
		t.Fatalf("slow queue depth %d exceeds bound %d", depth, queue)
	}
	if got := f.PeakQueued(); got > int64(queue) {
		t.Fatalf("peak buffered=%d exceeds queue=%d", got, queue)
	}
	producedBefore := f.produced.Load()
	time.Sleep(100 * time.Millisecond)
	if f.produced.Load() != producedBefore {
		t.Fatalf("producer advanced while slow consumer was stalled")
	}
	// The fast lane must have received data while the slow lane was stalled,
	// proving one slow layer does not starve another.
	if fast.chunks() == 0 {
		t.Fatalf("fast lane received nothing while slow lane was stalled")
	}

	close(blocked.gate)
	f.Wait()
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if blocked.closed != 1 || fast.closed != 1 {
		t.Fatalf("consumers not closed exactly once: %d/%d", blocked.closed, fast.closed)
	}
	if got := f.PeakQueued(); got > int64(queue) {
		t.Fatalf("peak buffered=%d exceeds queue=%d after drain", got, queue)
	}
}

// TestFanoutCancellationExitsAllGoroutines verifies cancellation unblocks a
// stalled consumer and every goroutine terminates.
func TestFanoutCancellationExitsAllGoroutines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := &slowConsumer{name: "slow", gate: make(chan struct{})}
	f, err := NewFanout(ctx, feedOf(bytes.Repeat([]byte("y"), 32), 4, "slow"), 1, blocked)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() { f.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutines did not exit after cancellation")
	}
}

// TestFanoutNoLossUnderSlowConsumer checks that no layer loses a chunk when a
// sibling layer is slower than the producer.
func TestFanoutNoLossUnderSlowConsumer(t *testing.T) {
	const chunks = 40
	var data []byte
	for i := 0; i < chunks; i++ {
		data = append(data, byte(i))
	}
	slow := &slowConsumer{name: "a", delay: 2 * time.Millisecond}
	fast := &slowConsumer{name: "b"}

	off := 0
	feed := FeedFunc(func(ctx context.Context) (Item, error) {
		if off >= len(data) {
			return Item{}, io.EOF
		}
		i := off
		off++
		layer := "a"
		if i%2 == 1 {
			layer = "b"
		}
		return Item{Layer: layer, Chunk: Chunk{Offset: int64(i), Data: data[i : i+1]}}, nil
	})
	f, err := NewFanout(context.Background(), feed, 2, slow, fast)
	if err != nil {
		t.Fatal(err)
	}
	f.Wait()
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if slow.chunks() != chunks/2 || fast.chunks() != chunks/2 {
		t.Fatalf("lost chunks: slow=%d fast=%d want %d each", slow.chunks(), fast.chunks(), chunks/2)
	}
	_, stats := f.Stats()
	for _, st := range stats {
		if st.Failed {
			t.Fatalf("consumer %s failed: %+v", st.Name, st)
		}
	}
}

// TestReaderPeakBounded uses a pipe producer that only advances when consumed.
func TestReaderPeakBounded(t *testing.T) {
	const chunk, queue = 5, 3
	pr, pw := io.Pipe()
	r, err := Open(pr, Options{ChunkBytes: chunk, Queue: queue})
	if err != nil {
		t.Fatal(err)
	}
	var wrote atomic.Int64
	go func() {
		for i := 0; i < 100; i++ {
			if _, err := pw.Write(make([]byte, chunk)); err != nil {
				return
			}
			wrote.Add(chunk)
		}
		_ = pw.Close()
	}()
	for i := 0; i < 10; i++ {
		if _, err := r.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	st := r.Stats()
	if st.PeakQueued > int64(queue+1) {
		t.Fatalf("peak queued %d exceeds queue+1=%d", st.PeakQueued, queue+1)
	}
	if bound := r.Bounds(); bound != int64((queue+1)*chunk) {
		t.Fatalf("bounds=%d", bound)
	}
	_ = pw.CloseWithError(io.ErrClosedPipe)
	_ = r.Close()
	if err := r.Err(); err != nil && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, ErrClosed) {
		t.Fatalf("unexpected reader err: %v", err)
	}
}

func TestOptionsValidation(t *testing.T) {
	if _, err := Open(bytes.NewReader(nil), Options{ChunkBytes: 0, Queue: 1}); err != nil {
		t.Fatalf("zero chunk should default: %v", err)
	}
	if _, err := Open(nil, Options{ChunkBytes: 1, Queue: 1}); err == nil {
		t.Fatal("nil source accepted")
	}
	if _, err := NewFanout(context.Background(), feedOf(nil, 1, "x"), 0, &slowConsumer{name: "x"}); err == nil {
		t.Fatal("zero queue accepted")
	}
	if _, err := NewFanout(context.Background(), feedOf(nil, 1, "x"), 1); err == nil {
		t.Fatal("no consumers accepted")
	}
	dup := &slowConsumer{name: "dup"}
	if _, err := NewFanout(context.Background(), feedOf(nil, 1, "dup"), 1, dup, dup); err == nil {
		t.Fatal("duplicate consumers accepted")
	}
}

func TestFanoutUnknownLayerFails(t *testing.T) {
	c := &slowConsumer{name: "known"}
	f, err := NewFanout(context.Background(), feedOf([]byte("abcd"), 1, "other"), 2, c)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Close()
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("unknown layer")) {
		t.Fatalf("want unknown layer error, got %v", err)
	}
}

func TestChunkLen(t *testing.T) {
	if got := (Chunk{Data: []byte("abc")}).Len(); got != 3 {
		t.Fatalf("len=%d", got)
	}
	if got := fmt.Sprint(Chunk{Offset: 3}); got == "" {
		t.Fatal("unexpected empty")
	}
}

// TestReaderLiveTokensBound proves the reader never has more than queue+1
// chunk payloads live, measured in bytes rather than only queue slots.
func TestReaderLiveTokensBound(t *testing.T) {
	const chunk, queue = 6, 2
	pr, pw := io.Pipe()
	r, err := Open(pr, Options{ChunkBytes: chunk, Queue: queue})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for i := 0; i < 50; i++ {
			if _, err := pw.Write(make([]byte, chunk)); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()
	for i := 0; i < 12; i++ {
		if _, err := r.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	st := r.Stats()
	if st.PeakBytes > int64((queue+1)*chunk) {
		t.Fatalf("peak bytes %d exceeds bound %d", st.PeakBytes, (queue+1)*chunk)
	}
	if st.PeakQueued > int64(queue+1) {
		t.Fatalf("peak queued %d exceeds %d", st.PeakQueued, queue+1)
	}
	_ = pw.CloseWithError(io.ErrClosedPipe)
	_ = r.Close()
}
