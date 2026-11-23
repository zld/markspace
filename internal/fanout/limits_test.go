package fanout

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"mediad/internal/merr"
)

func TestTransportLossAndReorderArePerLayer(t *testing.T) {
	ctx := context.Background()
	l := NewLadder([]LayerOptions{
		{Name: "clean"},
		{Name: "lossy", Transp: Transport{DropEvery: 5, SwapEvery: 7}},
		{Name: "clean2"},
	})
	var wg sync.WaitGroup
	for _, layer := range l.Layers() {
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
	for i := uint64(1); i <= 100; i++ {
		if err := l.Fanout(ctx, mkBlock(i, 256)); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	wg.Wait()
	stats := l.Stats()
	if stats[0].Dropped != 0 || stats[2].Dropped != 0 {
		t.Fatalf("clean layers saw loss: %+v %+v", stats[0], stats[2])
	}
	if stats[1].Dropped == 0 {
		t.Fatal("lossy layer recorded no loss")
	}
	if stats[1].Reordered == 0 {
		t.Fatal("lossy layer recorded no reorder recovery")
	}
	// Loss and reorder state must not leak into the clean layers.
	if stats[0].Reordered != 0 || stats[2].Reordered != 0 {
		t.Fatalf("reorder leaked into clean layers")
	}
}

func TestOversizeBlockRejected(t *testing.T) {
	l := NewLayer(LayerOptions{Name: "tiny", Buffer: 1024})
	err := l.Feed(context.Background(), mkBlock(1, 2048))
	if err == nil {
		t.Fatal("expected rejection of a block larger than the buffer")
	}
	if code := merr.CodeOf(err); code != merr.CodeBadInput {
		t.Fatalf("code = %s", code)
	}
}

func TestCanceledFeedReportsLayerCanceled(t *testing.T) {
	l := NewLayer(LayerOptions{Name: "x"})
	l.Cancel()
	err := l.Feed(context.Background(), mkBlock(1, 16))
	if code := merr.CodeOf(err); code != merr.CodeLayerCanceled {
		t.Fatalf("code = %s want %s (%v)", code, merr.CodeLayerCanceled, err)
	}
	var canceled *layerCancelError
	if !errors.As(err, &canceled) {
		t.Fatal("layer cancellation not distinguishable by errors.As")
	}
}

func TestPeakBufferNeverExceedsBudget(t *testing.T) {
	ctx := context.Background()
	const buf = 8192
	layer := NewLayer(LayerOptions{Name: "bounded", Buffer: buf, Slow: 200 * time.Microsecond})
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		for {
			if _, ok := layer.Next(context.Background()); !ok {
				return
			}
		}
	}()
	for i := uint64(1); i <= 100; i++ {
		if err := layer.Feed(ctx, mkBlock(i, 700)); err != nil {
			t.Fatal(err)
		}
		if got := layer.Stats().BufferedBytes; got > buf {
			t.Fatalf("buffered %d exceeds budget %d", got, buf)
		}
	}
	layer.Close()
	<-stop
	if peak := layer.Stats().PeakBytes; peak > buf {
		t.Fatalf("peak %d exceeds budget %d", peak, buf)
	}
}

func TestContextCancellationStopsFeed(t *testing.T) {
	layer := NewLayer(LayerOptions{Name: "stuck", Buffer: 1024})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := layer.Feed(ctx, mkBlock(1, 16)); err == nil {
		t.Fatal("expected cancellation error")
	}
}
