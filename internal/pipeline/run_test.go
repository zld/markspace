package pipeline

import (
	"testing"
	"time"
)

func TestRunBoundedAndOrdered(t *testing.T) {
	const n = 50
	var got []int
	st := Run(Config{Memory: 100},
		func(push func(Item)) {
			for i := 0; i < n; i++ {
				push(Item{SizeBytes: 30, Value: i})
			}
		},
		func(it Item) {
			time.Sleep(time.Millisecond) // slow consumer forces the bound
			got = append(got, it.Value.(int))
		})
	if st.Processed != n {
		t.Fatalf("processed = %d, want %d", st.Processed, n)
	}
	if st.PeakBytes > 100 {
		t.Fatalf("peak = %d exceeds memory 100", st.PeakBytes)
	}
	if st.PeakBytes < 90 {
		t.Fatalf("peak = %d, expected queue to fill near 100", st.PeakBytes)
	}
	for i := 0; i < n; i++ {
		if got[i] != i {
			t.Fatalf("order broken at %d: %v", i, got)
		}
	}
}

func TestRunOversizeItem(t *testing.T) {
	var count int
	st := Run(Config{Memory: 10},
		func(push func(Item)) {
			push(Item{SizeBytes: 50, Value: 1}) // larger than capacity
			push(Item{SizeBytes: 5, Value: 2})
		},
		func(it Item) { count++ })
	if count != 2 || st.Processed != 2 {
		t.Fatalf("processed = %d, want 2", st.Processed)
	}
}

func TestRunEmpty(t *testing.T) {
	st := Run(Config{Memory: 64}, func(push func(Item)) {}, func(it Item) {})
	if st.Processed != 0 || st.PeakBytes != 0 {
		t.Fatalf("got %+v, want zero run", st)
	}
}
