package relay

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
)

type recorder struct {
	mu      sync.Mutex
	written []rtp.Packet
	fail    error
	gate    chan struct{}
}

func (r *recorder) WriteRTP(p *rtp.Packet) error {
	if r.gate != nil {
		<-r.gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.written = append(r.written, *p)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.written)
}

func packet(seq uint16) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      1000 + uint32(seq)*1800,
			SSRC:           7,
			Marker:         seq%5 == 0,
		},
		Payload: []byte{0x10, byte(seq), 0xAA},
	}
}

func TestForwarderPreservesHeaderFields(t *testing.T) {
	rec := &recorder{}
	f, err := New(Config{QueueSize: 8}, rec)
	if err != nil {
		t.Fatal(err)
	}
	f.Start()
	for i := 0; i < 5; i++ {
		if err := f.Receive(packet(uint16(i))); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 5 {
		t.Fatalf("written=%d want 5", rec.count())
	}
	want := packet(4)
	got := rec.written[4]
	if got.SequenceNumber != want.SequenceNumber || got.Timestamp != want.Timestamp ||
		got.PayloadType != want.PayloadType || got.Marker != want.Marker || got.SSRC != want.SSRC {
		t.Fatalf("header not preserved: got %+v want %+v", got.Header, want.Header)
	}
	if string(got.Payload) != string(want.Payload) {
		t.Fatalf("payload not preserved: %x vs %x", got.Payload, want.Payload)
	}
	stats := f.Stats()
	if stats.Forwarded != 5 || stats.Dropped != 0 || stats.WriteFailed != 0 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestForwarderCountsQueueFullDrops(t *testing.T) {
	rec := &recorder{gate: make(chan struct{})}
	f, err := New(Config{QueueSize: 2}, rec)
	if err != nil {
		t.Fatal(err)
	}
	f.Start()
	// The writer blocks on the first write, so one packet is in flight and the
	// rest fill the two queue slots before overflowing.
	for i := 1; i <= 8; i++ {
		_ = f.Receive(packet(uint16(i)))
	}
	stats := f.Stats()
	if stats.Dropped == 0 {
		t.Fatalf("expected queue drops, stats=%+v", stats)
	}
	if stats.Received != stats.Enqueued+stats.Dropped {
		t.Fatalf("received must equal enqueued+dropped: %+v", stats)
	}
	if stats.LastError == "" {
		t.Fatalf("missing drop error: %+v", stats)
	}
	close(rec.gate)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.Close(ctx); err != nil {
		t.Fatal(err)
	}
	final := f.Stats()
	if final.Forwarded > uint64(rec.count()) {
		t.Fatalf("forwarded exceeds writes: %+v", final)
	}
	if final.Forwarded+final.WriteFailed != final.Enqueued {
		t.Fatalf("per-packet accounting mismatch: %+v", final)
	}
}

func TestForwarderCountsWriteFailures(t *testing.T) {
	rec := &recorder{fail: errors.New("boom")}
	f, err := New(Config{QueueSize: 4}, rec)
	if err != nil {
		t.Fatal(err)
	}
	f.Start()
	for i := 0; i < 4; i++ {
		if err := f.Receive(packet(uint16(i))); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.Close(ctx); err != nil {
		t.Fatal(err)
	}
	stats := f.Stats()
	if stats.WriteFailed != 4 {
		t.Fatalf("write failures=%d want 4", stats.WriteFailed)
	}
	if stats.Forwarded != 0 {
		t.Fatalf("forwarded=%d want 0 on total failure", stats.Forwarded)
	}
	if stats.Forwarded+stats.WriteFailed != stats.Enqueued {
		t.Fatalf("per-packet accounting mismatch: %+v", stats)
	}
	if stats.LastError == "" {
		t.Fatal("expected last error to be recorded")
	}
}

func TestForwarderRejectsAfterClose(t *testing.T) {
	f, err := New(Config{QueueSize: 2}, &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	f.Start()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Receive(packet(1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("receive after close: %v", err)
	}
}

func TestForwarderConcurrentReceiveAndClose(t *testing.T) {
	for round := 0; round < 20; round++ {
		rec := &recorder{}
		f, err := New(Config{QueueSize: 4}, rec)
		if err != nil {
			t.Fatal(err)
		}
		f.Start()
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					_ = f.Receive(packet(uint16(g*50 + i)))
				}
			}(g)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = f.Close(ctx)
		cancel()
		wg.Wait()
		f.Close(context.Background())
	}
}

func TestForwarderQueueDropsAreBoundedByCapacity(t *testing.T) {
	rec := &recorder{gate: make(chan struct{})}
	f, err := New(Config{QueueSize: 3}, rec)
	if err != nil {
		t.Fatal(err)
	}
	f.Start()
	for i := 0; i < 10; i++ {
		_ = f.Receive(packet(uint16(i)))
	}
	close(rec.gate)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.Close(ctx); err != nil {
		t.Fatal(err)
	}
	stats := f.Stats()
	// Capacity 3 plus the single write already in flight when the writer was
	// blocked is the most the forwarder can accept.
	if stats.Forwarded > 4 {
		t.Fatalf("forwarded=%d exceeds capacity+in-flight bound", stats.Forwarded)
	}
	if stats.Received != 10 {
		t.Fatalf("received=%d", stats.Received)
	}
	if stats.Enqueued+stats.Dropped != stats.Received {
		t.Fatalf("enqueued+dropped != received: %+v", stats)
	}
	if stats.Forwarded+stats.WriteFailed > stats.Enqueued {
		t.Fatalf("forwarded+failures exceed enqueued: %+v", stats)
	}
}
