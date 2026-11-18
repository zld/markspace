package stats

import (
	"sync"
	"testing"
)

func TestJitterZeroInput(t *testing.T) {
	j := NewJitterTracker(8000)
	if j.JitterTicks() != 0 || j.JitterMS() != 0 || j.Received() != 0 {
		t.Fatal("empty tracker must report zero")
	}
	j.Observe(100, 100) // single packet: no pair yet
	if j.JitterTicks() != 0 {
		t.Fatal("single packet must still report zero jitter")
	}
}

func TestJitterRFCExample(t *testing.T) {
	// Sender spacing is 20 ticks; arrivals alternate 20/30 ticks apart,
	// i.e. deviation magnitude 10 on every other packet.
	j := NewJitterTracker(8000)
	senderTS := []uint32{0, 20, 40, 60, 80}
	arrival := []uint32{0, 20, 50, 70, 100}
	for i := range senderTS {
		j.Observe(senderTS[i], arrival[i])
	}
	ticks := j.JitterTicks()
	// Expected smoothed value stays strictly positive and below 10.
	if ticks == 0 || ticks >= 10 {
		t.Fatalf("jitter ticks = %d, want 1..9", ticks)
	}
	ms := j.JitterMS()
	if ms <= 0 || ms >= 1.25 {
		t.Fatalf("jitter ms = %v, want within (0,1.25)", ms)
	}
	if j.Received() != 5 {
		t.Fatalf("received = %d, want 5", j.Received())
	}
}

func TestJitterPerfectStream(t *testing.T) {
	// Identical sender and arrival spacing everywhere: zero jitter.
	j := NewJitterTracker(8000)
	for i := 0; i < 20; i++ {
		j.Observe(uint32(i*160), uint32(i*160))
	}
	if j.JitterTicks() != 0 {
		t.Fatalf("constant-delay stream jitter = %d, want 0", j.JitterTicks())
	}
}

func TestJitterTimestampWraparound(t *testing.T) {
	j := NewJitterTracker(8000)
	j.Observe(0xFFFFFFF0, 0xFFFFFFF0)
	j.Observe(0x00000010, 0x00000010) // wraps by +32 ticks on both axes
	if j.JitterTicks() != 0 {
		t.Fatalf("wrapped constant-delay jitter = %d, want 0", j.JitterTicks())
	}
}

func TestJitterConcurrent(t *testing.T) {
	j := NewJitterTracker(8000)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				v := uint32(base*10000 + i*160)
				j.Observe(v, v)
			}
		}(w)
	}
	wg.Wait()
	if j.Received() != 4000 {
		t.Fatalf("received = %d, want 4000", j.Received())
	}
}
