package stream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBudgetTryTakeAndPeak(t *testing.T) {
	b := NewBudget(10)
	if !b.TryTake(6) {
		t.Fatal("take 6 should fit")
	}
	if b.TryTake(5) {
		t.Fatal("6+5 > 10 must be rejected")
	}
	if !b.TryTake(4) {
		t.Fatal("6+4 == 10 should fit")
	}
	if b.Peak() != 10 || b.Used() != 10 {
		t.Fatalf("peak=%d used=%d", b.Peak(), b.Used())
	}
	b.Give(4)
	if !b.TryTake(3) {
		t.Fatal("3 should fit after release")
	}
	if b.Peak() != 10 {
		t.Fatalf("peak should remain 10, got %d", b.Peak())
	}
}

func TestBudgetTakeLargerThanMaxErrors(t *testing.T) {
	b := NewBudget(4)
	err := b.Take(context.Background(), 8)
	if !errors.Is(err, ErrOverLimit) {
		t.Fatalf("want ErrOverLimit, got %v", err)
	}
}

func TestBudgetTakeBlocksThenWakes(t *testing.T) {
	b := NewBudget(8)
	if err := b.Take(context.Background(), 8); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		_ = b.Take(context.Background(), 4)
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("take must block while budget exhausted")
	case <-time.After(40 * time.Millisecond):
	}
	b.Give(8)
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("take did not wake after Give")
	}
}

func writeFile(t *testing.T, size int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "data.bin")
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*7 + 3)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDrainIntegrityAndBound(t *testing.T) {
	p := writeFile(t, 300_000)
	want, _ := os.ReadFile(p)
	wantSum := sha256.Sum256(want)

	budget := NewBudget(128 * 1024) // only two 64KiB chunks may be live
	h := sha256.New()
	total, err := Drain(context.Background(), p, budget, 64<<10, func(_ int64, p []byte) error {
		_, err := h.Write(p)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != int64(len(want)) {
		t.Fatalf("total=%d want %d", total, len(want))
	}
	if !bytes.Equal(h.Sum(nil), wantSum[:]) {
		t.Fatal("streamed hash mismatch")
	}
	if budget.Peak() > budget.Max() {
		t.Fatalf("peak %d exceeds cap %d", budget.Peak(), budget.Max())
	}
}

func TestSlowConsumerExertsBackpressure(t *testing.T) {
	p := writeFile(t, 1<<20) // 1 MiB
	const chunk = 32 << 10
	budget := NewBudget(2 * chunk) // at most two chunk reservations
	chunks, errs := StreamFile(context.Background(), p, budget, chunk)

	first := <-chunks
	// Hold first chunk; drain one more so the producer is blocked on send
	// while holding at most the budget.
	second := <-chunks
	if budget.Used() > budget.Max() {
		t.Fatalf("used=%d exceeds max=%d", budget.Used(), budget.Max())
	}

	// A third reservation must be impossible while both chunks are held.
	if budget.TryTake(chunk) {
		t.Fatal("producer allocated beyond the bounded budget")
	}

	// Slow consumer: wait, then release; stream must complete.
	time.Sleep(30 * time.Millisecond)
	first.Release()
	second.Release()

	n := 2
	for c := range chunks {
		if budget.Used() > budget.Max() {
			t.Fatal("budget exceeded during drain")
		}
		n++
		c.Release()
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if n < 30 {
		t.Fatalf("expected many chunks, got %d", n)
	}
	if budget.Peak() > budget.Max() {
		t.Fatalf("peak=%d max=%d", budget.Peak(), budget.Max())
	}
}

func TestMissingFileError(t *testing.T) {
	budget := NewBudget(1 << 20)
	chunks, errs := StreamFile(context.Background(), filepath.Join(t.TempDir(), "missing"), budget, 4096)
	for range chunks {
	}
	if err := <-errs; err == nil {
		t.Fatal("expected open error")
	}
}
