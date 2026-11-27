package ingest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamChunksContentAndOffset(t *testing.T) {
	payload := make([]byte, 200_000)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	cfg := ReaderConfig{ChunkSize: 4096, QueueDepth: 2}
	chunks, stats := StreamChunks(context.Background(), bytes.NewReader(payload), cfg)

	var got bytes.Buffer
	var wantOffset int64
	for ch := range chunks {
		if ch.Offset != wantOffset {
			t.Fatalf("offset: got %d want %d", ch.Offset, wantOffset)
		}
		got.Write(ch.Data)
		wantOffset += int64(len(ch.Data))
		ch.Release()
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatal("reassembled content differs from source")
	}
	if stats.BytesRead != int64(len(payload)) {
		t.Fatalf("bytes read: got %d want %d", stats.BytesRead, len(payload))
	}
	if err := Guard(stats, cfg); err != nil {
		t.Fatalf("buffer ceiling violated: %v", err)
	}
	if stats.PeakInFlight > int64(cfg.PeakBytes()) {
		t.Fatalf("peak %d > ceiling %d", stats.PeakInFlight, cfg.PeakBytes())
	}
}

// TestSlowConsumerBackpressure proves the queue is bounded: a deliberately slow
// consumer must make the producer wait, and peak buffered bytes stay at or
// below the configured ceiling even though the source reader is instant.
func TestSlowConsumerBackpressure(t *testing.T) {
	payload := make([]byte, 2_000_000) // ~488 chunks at 4 KiB
	cfg := ReaderConfig{ChunkSize: 4096, QueueDepth: 2}
	chunks, stats := StreamChunks(context.Background(), bytes.NewReader(payload), cfg)

	consumed := 0
	for ch := range chunks {
		time.Sleep(2 * time.Millisecond) // slow consumer
		consumed++
		ch.Release()
		if err := Guard(stats, cfg); err != nil {
			t.Fatalf("peak exceeded ceiling mid-stream: %v", err)
		}
	}
	if consumed == 0 {
		t.Fatal("no chunks consumed")
	}
	if err := Guard(stats, cfg); err != nil {
		t.Fatalf("peak exceeded ceiling: %v", err)
	}
	if stats.PeakInFlight > int64(cfg.PeakBytes()) {
		t.Fatalf("peak buffered %d exceeds ceiling %d", stats.PeakInFlight, cfg.PeakBytes())
	}
	// With depth 2, at most 3 buffers (queued + in-progress) may ever exist.
	if stats.PeakInFlight > 3*int64(cfg.ChunkSize) {
		t.Fatalf("peak %d implies unbounded buffering", stats.PeakInFlight)
	}
}

// TestProducerBlockedOnFullQueue directly observes back-pressure by stopping
// the consumer and waiting long enough that a bounded producer must stall.
func TestProducerBlockedOnFullQueue(t *testing.T) {
	payload := make([]byte, 1_000_000)
	cfg := ReaderConfig{ChunkSize: 1024, QueueDepth: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chunks, stats := StreamChunks(ctx, bytes.NewReader(payload), cfg)

	// Drain exactly QueueDepth+1 chunks (queue capacity plus the one the
	// producer is filling), then stop consuming.
	for i := 0; i < cfg.QueueDepth+1; i++ {
		ch := <-chunks
		ch.Release()
	}
	// Give the producer time to run; an unbounded implementation would keep
	// reading far past the ceiling.
	time.Sleep(50 * time.Millisecond)
	stalledRead := atomic.LoadInt64(&stats.BytesRead)
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt64(&stats.BytesRead); got != stalledRead {
		t.Fatalf("producer kept reading without consumer: %d -> %d", stalledRead, got)
	}
	if err := Guard(stats, cfg); err != nil {
		t.Fatalf("ceiling violated while stalled: %v", err)
	}
}

func TestStreamChunksCancellationReleases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// A reader that blocks forever until canceled.
	pr, pw := io.Pipe()
	defer pw.Close()
	cfg := ReaderConfig{ChunkSize: 256, QueueDepth: 1}
	chunks, _ := StreamChunks(ctx, pr, cfg)

	go func() {
		_, _ = pw.Write(make([]byte, 1024))
	}()
	// Consume one chunk so the goroutine enters its loop, then cancel.
	ch := <-chunks
	ch.Release()
	cancel()

	// Channel must close promptly and the range loop must terminate.
	done := make(chan struct{})
	go func() {
		for c := range chunks {
			c.Release()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("producer did not stop after context cancellation")
	}
}

type errReader struct{ failAfter int }

func (e *errReader) Read(p []byte) (int, error) {
	if e.failAfter <= 0 {
		return 0, errors.New("boom")
	}
	n := len(p)
	if n > e.failAfter {
		n = e.failAfter
	}
	for i := range p[:n] {
		p[i] = 'x'
	}
	e.failAfter -= n
	if e.failAfter == 0 {
		return n, errors.New("boom")
	}
	return n, nil
}

func TestStreamChunksReadErrorClosesChannel(t *testing.T) {
	cfg := ReaderConfig{ChunkSize: 128, QueueDepth: 2}
	chunks, _ := StreamChunks(context.Background(), &errReader{failAfter: 300}, cfg)
	n := 0
	for ch := range chunks {
		n += len(ch.Data)
		ch.Release()
	}
	if n != 300 {
		t.Fatalf("want 300 bytes before error, got %d", n)
	}
}

func TestHashFileDeterministic(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/src.bin"
	payload := []byte(strings.Repeat("deterministic-source-", 500))
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := ReaderConfig{ChunkSize: 2048, QueueDepth: 3}
	sum1, size1, st1, err := HashFile(context.Background(), path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	sum2, size2, st2, err := HashFile(context.Background(), path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sum1 != sum2 || size1 != size2 || size1 != int64(len(payload)) {
		t.Fatal("digest/size not stable across runs")
	}
	if err := Guard(st1, cfg); err != nil {
		t.Fatalf("first hash ceiling violated: %v", err)
	}
	if err := Guard(st2, cfg); err != nil {
		t.Fatalf("second hash ceiling violated: %v", err)
	}
	if HexDigest(sum1) == "" || len(HexDigest(sum1)) != 64 {
		t.Fatal("hex digest malformed")
	}
}
