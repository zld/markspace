package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOrderedPoolYieldsChronologicalOrder(t *testing.T) {
	tasks := []Task[int]{
		{Index: 0, Run: func(context.Context) (int, error) { time.Sleep(30 * time.Millisecond); return 0, nil }},
		{Index: 1, Run: func(context.Context) (int, error) { return 1, nil }},
		{Index: 2, Run: func(context.Context) (int, error) { time.Sleep(10 * time.Millisecond); return 2, nil }},
	}
	var got []int
	err := OrderedPool(context.Background(), 3, tasks, func(index, value int) error {
		if index != value {
			t.Fatalf("index/value mismatch %d/%d", index, value)
		}
		got = append(got, value)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("yield order=%v", got)
	}
}

func TestOrderedPoolStopsAndPropagatesYieldError(t *testing.T) {
	tasks := make([]Task[int], 5)
	for i := range tasks {
		i := i
		tasks[i] = Task[int]{Index: i, Run: func(context.Context) (int, error) { return i, nil }}
	}
	var yielded []int
	err := OrderedPool(context.Background(), 2, tasks, func(index, value int) error {
		yielded = append(yielded, index)
		if index == 0 {
			return ErrStoppedAfter
		}
		return nil
	})
	if !errors.Is(err, ErrStoppedAfter) {
		t.Fatalf("err=%v, want ErrStoppedAfter", err)
	}
	if len(yielded) != 1 || yielded[0] != 0 {
		t.Fatalf("yielded=%v, want only first chronological result", yielded)
	}
}
