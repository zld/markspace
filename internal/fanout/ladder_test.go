package fanout

import (
	"context"
	"sync"
	"testing"
	"time"

	"mediad/internal/media"
)

func mkBlock(seq uint64, size int) media.Block {
	return media.Block{
		Track:     1,
		TrackType: media.TrackVideo,
		Seq:       seq,
		TimeNs:    int64(seq) * 40e6,
		Keyframe:  seq%25 == 0,
		Data:      make([]byte, size),
	}
}

// drain consumes a layer in the background until it closes.
func drain(ctx context.Context, l *Layer) *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			if _, ok := l.Next(ctx); !ok {
				return
			}
		}
	}()
	return &wg
}

func TestLadderNamesAndBuffers(t *testing.T) {
	l := DefaultLadder(0)
	defer l.Close()
	names := l.Names()
	want := []string{"360p", "720p", "1080p"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v", names)
		}
	}
	for _, layer := range l.Layers() {
		if layer.opts.Buffer != BufferBytes {
			t.Fatalf("%s buffer = %d, want %d", layer.Name(), layer.opts.Buffer, BufferBytes)
		}
	}
}

func TestFanoutDeliveryAndIsolation(t *testing.T) {
	ctx := context.Background()
	l := NewLadder([]LayerOptions{
		{Name: "a"},
		{Name: "b"},
		{Name: "c"},
	})
	done := make(map[string]*sync.WaitGroup)
	for _, layer := range l.Layers() {
		done[layer.Name()] = drain(ctx, layer)
	}
	for i := uint64(1); i <= 200; i++ {
		if err := l.Fanout(ctx, mkBlock(i, 1024)); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	for name, wg := range done {
		wg.Wait()
		_ = name
	}
	for _, s := range l.Stats() {
		if s.Delivered != 200 {
			t.Fatalf("%s delivered %d want 200", s.Name, s.Delivered)
		}
		if s.PeakBytes > BufferBytes {
			t.Fatalf("%s peak %d exceeds budget", s.Name, s.PeakBytes)
		}
	}
}

func TestSlowConsumerAppliesBackpressure(t *testing.T) {
	ctx := context.Background()
	const buf = 4096
	slow := NewLayer(LayerOptions{Name: "slow", Buffer: buf, Slow: time.Millisecond})
	defer slow.Cancel()
	go func() {
		for {
			if _, ok := slow.Next(context.Background()); !ok {
				return
			}
		}
	}()
	// 1024-byte blocks: the 4 KiB budget holds four. Feeding many more must
	// block, and the wait must be recorded rather than blocks dropped.
	start := time.Now()
	for i := uint64(1); i <= 40; i++ {
		if err := slow.Feed(ctx, mkBlock(i, 1024)); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	s := slow.Stats()
	if s.Offered != 40 {
		t.Fatalf("offered = %d", s.Offered)
	}
	if s.Dropped != 0 {
		t.Fatalf("slow consumer dropped %d blocks instead of backpressuring", s.Dropped)
	}
	if s.BackpressureNs == 0 {
		t.Fatal("no backpressure recorded")
	}
	if elapsed < time.Millisecond {
		t.Fatalf("expected measurable publisher stall, got %v", elapsed)
	}
}

func TestCancelStopsOnlyOneLayer(t *testing.T) {
	ctx := context.Background()
	l := NewLadder([]LayerOptions{
		{Name: "live-1"},
		{Name: "doomed"},
		{Name: "live-2"},
	})
	var wg sync.WaitGroup
	for _, layer := range l.Layers() {
		if layer.Name() == "doomed" {
			continue
		}
		wg.Add(1)
		go func(layer *Layer) {
			defer wg.Done()
			for {
				if _, ok := layer.Next(ctx); !ok {
					return
				}
			}
		}(layer)
	}
	for i := uint64(1); i <= 50; i++ {
		if err := l.Fanout(ctx, mkBlock(i, 512)); err != nil {
			t.Fatal(err)
		}
	}
	l.Layers()[1].Cancel()
	// Publishing continues for the surviving layers.
	for i := uint64(51); i <= 100; i++ {
		if err := l.Fanout(ctx, mkBlock(i, 512)); err != nil {
			t.Fatalf("fanout after cancel: %v", err)
		}
	}
	l.Close()
	wg.Wait()
	stats := l.Stats()
	if !stats[1].Canceled {
		t.Fatal("doomed layer not marked canceled")
	}
	for _, idx := range []int{0, 2} {
		if stats[idx].Canceled {
			t.Fatalf("layer %s wrongly canceled", stats[idx].Name)
		}
		if stats[idx].Delivered != 100 {
			t.Fatalf("layer %s delivered %d want 100", stats[idx].Name, stats[idx].Delivered)
		}
	}
}
