package pipe

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamPreservesOrderAndConfirmsCursor(t *testing.T) {
	var got []int64
	var count atomic.Int64
	h := func(_ context.Context, r Record) error {
		got = append(got, r.Cursor)
		count.Add(1)
		return nil
	}
	records := []Record{{Cursor: 1}, {Cursor: 2}, {Cursor: 3}}
	cursor, peak, p95, err := Process(context.Background(), Config{Queue: 2, MemoryBytes: 1 << 20}, records, h)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != 3 || count.Load() != 3 {
		t.Fatalf("cursor=%d count=%d", cursor, count.Load())
	}
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("order lost: %v", got)
	}
	if peak <= 0 || p95 < 0 {
		t.Fatalf("bad metrics peak=%d p95=%v", peak, p95)
	}
}

func TestSlowConsumerAppliesBackpressureWithoutDrops(t *testing.T) {
	var n atomic.Int64
	h := func(context.Context, Record) error {
		time.Sleep(2 * time.Millisecond)
		n.Add(1)
		return nil
	}
	records := make([]Record, 8)
	for i := range records {
		records[i] = Record{Cursor: int64(i + 1), Payload: []byte("x")}
	}
	start := time.Now()
	cursor, _, _, err := Process(context.Background(), Config{Queue: 1, MemoryBytes: 1 << 20}, records, h)
	if err != nil {
		t.Fatal(err)
	}
	if n.Load() != 8 || cursor != 8 {
		t.Fatalf("dropped records: n=%d cursor=%d", n.Load(), cursor)
	}
	if time.Since(start) < 14*time.Millisecond {
		t.Fatalf("slow consumer did not visibly apply backpressure")
	}
}

func TestCancelRetainsLastConfirmedCursor(t *testing.T) {
	st := NewStream(Config{Queue: 1, MemoryBytes: 1 << 20}, func(ctx context.Context, r Record) error {
		if r.Cursor == 2 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	st.Start()
	if err := st.Send(context.Background(), Record{Cursor: 1}); err != nil {
		t.Fatal(err)
	}
	// Give the consumer time to confirm cursor 1 and block on cursor 2.
	time.Sleep(10 * time.Millisecond)
	if err := st.Send(context.Background(), Record{Cursor: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.Send(context.Background(), Record{Cursor: 3}); err != nil {
		t.Fatal(err) // fills the one-slot queue while cursor 2 is handled
	}
	go func() {
		time.Sleep(5 * time.Millisecond)
		st.Cancel()
	}()
	err := st.Send(context.Background(), Record{Cursor: 4})
	if err == nil {
		t.Fatal("expected blocked send to be cancelled")
	}
	_ = st.Close()
	cursor, ok := st.LastCursor()
	if !ok || cursor != 1 {
		t.Fatalf("cursor=%d ok=%v, want last confirmed 1", cursor, ok)
	}
}

func TestBudgetRejectsOversizedRecord(t *testing.T) {
	b := NewBudget(10)
	err := b.Acquire(context.Background(), 11)
	if err == nil {
		t.Fatal("expected memory budget error")
	}
}
