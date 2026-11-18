package forward

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRelayBackpressureDrops(t *testing.T) {
	r := NewRelay(8, OverflowDrop)
	r.SetBackpressure(Backpressure{Every: 4, Pause: 50 * time.Millisecond})
	var consumed int64
	go r.Run(context.Background(), func([]byte) { atomic.AddInt64(&consumed, 1) })

	tk := time.NewTicker(time.Millisecond)
	defer tk.Stop()
	dropped := 0
	for i := 0; i < 60; i++ {
		<-tk.C
		ok, err := r.Enqueue(context.Background(), makeRaw(uint16(i), uint32(i)))
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			dropped++
		}
	}
	r.Close()
	r.Wait()
	if r.Stats().Dropped == 0 {
		t.Fatalf("backpressure produced no drops: %+v", r.Stats())
	}
	if dropped != int(r.Stats().Dropped) {
		t.Fatalf("drop counter mismatch: returned=%d stats=%d", dropped, r.Stats().Dropped)
	}
}

func TestRelayNoBackpressureDrainsAll(t *testing.T) {
	r := NewRelay(128, OverflowDrop)
	var consumed int64
	sink := func([]byte) { atomic.AddInt64(&consumed, 1) }
	done := make(chan struct{})
	go func() {
		r.Run(context.Background(), sink)
		close(done)
	}()
	for i := 0; i < 100; i++ {
		if _, err := r.Enqueue(context.Background(), makeRaw(uint16(i), uint32(i))); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	<-done
	if atomic.LoadInt64(&consumed) != 100 {
		t.Fatalf("consumer saw %d packets, want 100", atomic.LoadInt64(&consumed))
	}
	st := r.Stats()
	if st.Dropped != 0 || st.Forwarded != 100 {
		t.Fatalf("expected full forwarding, got %+v", st)
	}
}
