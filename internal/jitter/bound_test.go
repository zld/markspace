package jitter

import "testing"

// TestLongStreamStaysBounded pushes 50k in-order packets while periodically
// draining the output queue and asserts the reorder slots never exceed the
// configured window, proving memory stays bounded for long streams.
func TestLongStreamStaysBounded(t *testing.T) {
	b, _ := New(Config{Window: 8, Node: "j"})
	const n = 50000
	totalReleased := 0
	for i := 0; i < n; i++ {
		if err := b.Push(frame(uint64(i), int64(i))); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
		if b.Buffered() > 8 {
			t.Fatalf("buffered %d exceeds window at i=%d", b.Buffered(), i)
		}
		for {
			if _, ok := b.Release(); !ok {
				break
			}
			totalReleased++
		}
	}
	if totalReleased != n {
		t.Fatalf("released %d events, want %d (no loss on a clean stream)", totalReleased, n)
	}
	if b.Buffered() != 0 {
		t.Fatalf("buffered = %d, want 0 after full drain", b.Buffered())
	}
}
