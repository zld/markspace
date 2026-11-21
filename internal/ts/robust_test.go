package ts

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestRandomGarbageDoesNotPanic feeds adversarial byte streams through the
// scanner; no input may panic or hang, and every call terminates.
func TestRandomGarbageDoesNotPanic(t *testing.T) {
	seeds := []int64{1, 2, 7, 42, 99, 1024}
	for _, sd := range seeds {
		rng := rand.New(rand.NewSource(sd))
		for size := 0; size <= 2000; size += 137 {
			buf := make([]byte, size)
			rng.Read(buf)
			// Sprinkle sync bytes to encourage partial framing.
			for i := 0; i < len(buf); i += 17 {
				buf[i] = SyncByte
			}
			sc := NewScanner(bytes.NewReader(buf))
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panic on seed=%d size=%d: %v", sd, size, r)
					}
				}()
				for sc.Next() != nil {
				}
				_ = sc.Err()
			}()
		}
	}
}

// TestTruncatedEveryBoundary never panics when streams end mid-packet.
func TestTruncatedEveryBoundary(t *testing.T) {
	base := make([]byte, PacketSize*8)
	for i := range base {
		base[i] = byte(i)
		if i%PacketSize == 0 {
			base[i] = SyncByte
		}
	}
	for cut := 1; cut < len(base); cut++ {
		sc := NewScanner(bytes.NewReader(base[:cut]))
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic at cut=%d: %v", cut, r)
				}
			}()
			for sc.Next() != nil {
			}
		}()
	}
}
