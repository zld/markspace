package replay

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRegisterOnce(t *testing.T) {
	c := New()
	if err := c.Register("lic-1", "asset", 10); err != nil {
		t.Fatal(err)
	}
	if err := c.Register("lic-1", "asset", 11); !errors.Is(err, ErrReplay) {
		t.Fatalf("second register: %v, want ErrReplay", err)
	}
	if c.Len() != 1 {
		t.Fatalf("cache holds %d entries, want 1", c.Len())
	}
	e, ok := c.Lookup("lic-1")
	if !ok || e.FirstUseMillis != 10 {
		t.Fatalf("unexpected entry %+v (ok=%v)", e, ok)
	}
}

func TestConcurrentRegisterHasSingleWinner(t *testing.T) {
	c := New()
	const racers = 32
	var wins int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			err := c.Register("lic-shared", "asset", int64(n))
			switch {
			case err == nil:
				atomic.AddInt64(&wins, 1)
			case errors.Is(err, ErrReplay):
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d concurrent registrations succeeded, want exactly 1", wins)
	}
	if c.Len() != 1 {
		t.Fatalf("cache holds %d entries, want 1", c.Len())
	}
}

func TestEmptySerialRejected(t *testing.T) {
	c := New()
	if err := c.Register("", "asset", 0); !errors.Is(err, ErrEmptyID) {
		t.Fatalf("got %v, want ErrEmptyID", err)
	}
	if c.Len() != 0 {
		t.Fatalf("empty serial was stored")
	}
}

func TestDistinctSerialsBothSucceed(t *testing.T) {
	c := New()
	if err := c.Register("lic-1", "a", 1); err != nil {
		t.Fatal(err)
	}
	if err := c.Register("lic-2", "a", 2); err != nil {
		t.Fatal(err)
	}
	if !c.Contains("lic-1") || !c.Contains("lic-2") || c.Contains("lic-3") {
		t.Fatal("Contains disagrees with registered serials")
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
}
