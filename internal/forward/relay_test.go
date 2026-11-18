package forward

import (
	"context"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func makeRaw(seq uint16, ts uint32) []byte {
	p := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 1}, Payload: []byte{1, 2, 3}}
	b, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

func TestRelayDropMode(t *testing.T) {
	r := NewRelay(2, OverflowDrop)
	ctx := context.Background()
	// Fill beyond capacity: surplus packets are dropped and counted.
	results := make([]bool, 5)
	for i := 0; i < 5; i++ {
		ok, _ := r.Enqueue(ctx, makeRaw(uint16(i), uint32(i)*160))
		results[i] = ok
	}
	if r.Pending() != 2 {
		t.Fatalf("pending = %d, want 2", r.Pending())
	}
	got := make([][]byte, 0, 2)
	r.Close()
	r.Run(context.Background(), func(raw []byte) { got = append(got, raw) })
	if len(got) != 2 {
		t.Fatalf("forwarded %d packets, want 2", len(got))
	}
	st := r.Stats()
	if st.Enqueued != 5 || st.Dropped != 3 || st.Forwarded != 2 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}

func TestRelayBlockModeUnblocksOnClose(t *testing.T) {
	r := NewRelay(1, OverflowBlock)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := r.Enqueue(ctx, makeRaw(0, 0)); err != nil {
		t.Fatal(err)
	}
	// Consumer drains in the background so shutdown can always complete.
	go r.Run(context.Background(), func([]byte) {})
	// Second enqueue would block; Close must release it without deadlock.
	done := make(chan error, 1)
	go func() {
		_, err := r.Enqueue(ctx, makeRaw(1, 160))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	r.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked Enqueue did not unblock on Close")
	}
	r.Wait()
}

func TestRelayShutdownDrains(t *testing.T) {
	r := NewRelay(8, OverflowDrop)
	for i := 0; i < 6; i++ {
		_, _ = r.Enqueue(context.Background(), makeRaw(uint16(i), 0))
	}
	forwarded := 0
	runDone := make(chan struct{})
	go func() {
		r.Run(context.Background(), func([]byte) { forwarded++ })
		close(runDone)
	}()
	r.Close()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after Close")
	}
	r.Wait()
	if forwarded != 6 {
		t.Fatalf("drained %d, want 6", forwarded)
	}
}

func TestRelayMalformedAndOrdering(t *testing.T) {
	r := NewRelay(16, OverflowDrop)
	ctx := context.Background()
	if ok, err := r.Enqueue(ctx, []byte{0x40}); err == nil || ok {
		t.Fatal("malformed packet must be rejected")
	}
	seqs := []uint16{10, 11, 12, 11, 9, 13} // one duplicate, one first-time late packet
	for _, s := range seqs {
		_, _ = r.Enqueue(ctx, makeRaw(s, uint32(s)*160))
	}
	st := r.Stats()
	if st.Malformed != 1 {
		t.Errorf("malformed = %d, want 1", st.Malformed)
	}
	if st.Duplicates != 1 {
		t.Errorf("duplicates = %d, want 1", st.Duplicates)
	}
	if st.Reordered != 1 {
		t.Errorf("reordered = %d, want 1", st.Reordered)
	}
}

func TestRelayPreservesWireBytes(t *testing.T) {
	r := NewRelay(4, OverflowDrop)
	raw := makeRaw(42, 7000)
	if _, err := r.Enqueue(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	var out []byte
	r.Close()
	r.Run(context.Background(), func(b []byte) { out = b })
	if string(out) != string(raw) {
		t.Fatal("relay rewrote packet bytes")
	}
}
