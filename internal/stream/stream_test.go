package stream

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueueCapacityTwoAppliesBackpressure(t *testing.T) {
	q := NewQueue(2)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := q.Push(ctx, Block{Index: i}); err != nil {
			t.Fatal(err)
		}
	}
	if q.Peak() != 2 {
		t.Fatalf("peak=%d", q.Peak())
	}
	timeout, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := q.Push(timeout, Block{Index: 2}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected backpressure timeout, got %v", err)
	}
	b, ok, err := q.Pop(ctx)
	if err != nil || !ok || b.Index != 0 {
		t.Fatalf("pop=%+v ok=%v err=%v", b, ok, err)
	}
	if err := q.Push(ctx, Block{Index: 2}); err != nil {
		t.Fatal(err)
	}
	if q.Peak() > 2 {
		t.Fatalf("peak exceeded capacity: %d", q.Peak())
	}
}

func TestPipelineSlowConsumerPeakAndRerun(t *testing.T) {
	run := func() ([]byte, PipelineStats) {
		q := NewQueue(2)
		var got []byte
		p := &Pipeline{
			Queue: q,
			Produce: func(ctx context.Context, emit func(Block) error) error {
				for i := 0; i < 8; i++ {
					if err := emit(Block{Index: i, Data: []byte{byte('a' + i)}}); err != nil {
						return err
					}
				}
				return nil
			},
			Consume: SlowConsume(time.Millisecond, func(b Block) error {
				got = append(got, b.Data...)
				return nil
			}),
		}
		stats, err := p.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return got, stats
	}
	first, s1 := run()
	second, s2 := run()
	if string(first) != "abcdefgh" || string(second) != string(first) {
		t.Fatalf("non-deterministic runs: %q vs %q", first, second)
	}
	if s1.QueuePeak > 2 || s2.QueuePeak > 2 || s1.Consumed != 8 {
		t.Fatalf("bad stats %+v %+v", s1, s2)
	}
}

func TestPipelineCancelCleansStaging(t *testing.T) {
	st, err := NewStaging(t.TempDir(), "cancel-")
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.CreateFile("unpublished/block.bin")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Pipeline{
		Queue: NewQueue(2), Staging: st,
		Produce: func(ctx context.Context, emit func(Block) error) error {
			return emit(Block{Index: 0})
		},
		Consume: func(ctx context.Context, b Block) error { <-ctx.Done(); return ctx.Err() },
	}
	_, err = p.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if _, err := os.Stat(st.Dir); !os.IsNotExist(err) {
		t.Fatalf("staging %s must be removed, stat err=%v", st.Dir, err)
	}
}

func TestPipelineConsumerErrorStopsAndCleans(t *testing.T) {
	st, err := NewStaging(t.TempDir(), "error-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir, "tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var produced int
	p := &Pipeline{
		Queue: NewQueue(2), Staging: st,
		Produce: func(ctx context.Context, emit func(Block) error) error {
			for i := 0; i < 1000; i++ {
				if err := emit(Block{Index: i}); err != nil {
					return err
				}
				produced++
			}
			return nil
		},
		Consume: func(ctx context.Context, b Block) error {
			if b.Index == 1 {
				return errors.New("decode failed")
			}
			return nil
		},
	}
	_, err = p.Run(context.Background())
	if !errors.Is(err, ErrConsumer) {
		t.Fatalf("want consumer error, got %v", err)
	}
	if _, err := os.Stat(st.Dir); !os.IsNotExist(err) {
		t.Fatalf("staging must be removed after consumer error")
	}
}
