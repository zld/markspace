package stream_test

import (
	"context"
	"testing"
	"time"

	"github.com/pion/rtp"

	"mq/internal/container"
	"mq/internal/fixtures"
	"mq/internal/stream"
	"mq/internal/timeline"
)

func packetSource(pkts []*rtp.Packet) stream.SourceFunc {
	return func(yield func(*rtp.Packet) error) error {
		for _, p := range pkts {
			if err := yield(p); err != nil {
				return err
			}
		}
		return nil
	}
}

func composedMP4(t *testing.T) (*timeline.Timeline, *timeline.Asset) {
	t.Helper()
	asset, err := container.Parse(fixtures.GoodMP4(), container.ParseOptions{MaxTracks: 8})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tl, err := timeline.Compose(asset, timeline.ComposeOptions{})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	return tl, asset
}

func TestBackpressureBoundedQueue(t *testing.T) {
	tl, asset := composedMP4(t)
	res := stream.Run(context.Background(), stream.SamplesSource(tl, asset), stream.Options{
		QueueCapacity: 4,
		SlowConsumer:  5 * time.Millisecond,
	})
	if res.Err != nil {
		t.Fatalf("pipeline: %v", res.Err)
	}
	if res.Confirmed != 24 {
		t.Fatalf("confirmed=%d want 24", res.Confirmed)
	}
	if res.PeakQueue > 4 {
		t.Fatalf("peak=%d exceeds capacity 4", res.PeakQueue)
	}
	if len(res.Timestamps) != 24 {
		t.Fatalf("timestamps=%d", len(res.Timestamps))
	}
}

func TestCancelReclaimsAndKeepsBoundary(t *testing.T) {
	tl, asset := composedMP4(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *stream.Result, 1)
	go func() {
		done <- stream.Run(ctx, stream.SamplesSource(tl, asset), stream.Options{
			QueueCapacity: 4,
			SlowConsumer:  50 * time.Millisecond,
		})
	}()
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if res.Confirmed >= 24 {
			t.Fatalf("cancel did not stop consumption: confirmed=%d", res.Confirmed)
		}
		if res.Confirmed > len(res.Timestamps) {
			t.Fatalf("confirmed=%d exceeds observed timestamps=%d",
				res.Confirmed, len(res.Timestamps))
		}
		if res.PeakQueue > 4 {
			t.Fatalf("peak=%d exceeds capacity", res.PeakQueue)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("pipeline goroutines not reclaimed after cancel")
	}
}

func TestWrapAndDuplicateTimestamps(t *testing.T) {
	res := stream.Run(context.Background(), packetSource(fixtures.WrapDupPackets()), stream.Options{
		QueueCapacity: 2,
	})
	if res.Err != nil {
		t.Fatalf("pipeline: %v", res.Err)
	}
	want := []int64{0xFFFFFFF0, 0x100000010, 0x100000010, 0x100000020}
	if len(res.Timestamps) != len(want) {
		t.Fatalf("timestamps=%v", res.Timestamps)
	}
	for i, w := range want {
		if res.Timestamps[i] != w {
			t.Fatalf("timestamp[%d]=%d want %d", i, res.Timestamps[i], w)
		}
	}
	if len(res.Duplicates) != 1 || res.Duplicates[0] != 2 {
		t.Fatalf("duplicates=%v want [2]", res.Duplicates)
	}
}

func TestTimestampRegressionDetected(t *testing.T) {
	pkts := []*rtp.Packet{
		{Header: rtp.Header{Version: 2, Timestamp: 100, SSRC: 1}},
		{Header: rtp.Header{Version: 2, Timestamp: 50, SSRC: 1}},
	}
	res := stream.Run(context.Background(), packetSource(pkts), stream.Options{QueueCapacity: 2})
	if res.Err == nil {
		t.Fatalf("expected regression error")
	}
}

func TestReadChunksOffsetsAndCancel(t *testing.T) {
	buf := make([]byte, 1000)
	out := make(chan stream.Chunk, 8)
	done := make(chan error, 1)
	go func() {
		done <- stream.ReadChunks(context.Background(), buf, 100, out)
	}()
	var got int64
	var n int
	for c := range out {
		if c.Offset != got {
			t.Fatalf("chunk offset=%d want %d", c.Offset, got)
		}
		got += int64(len(c.Data))
		n++
	}
	if err := <-done; err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != 1000 || n != 10 {
		t.Fatalf("read %d bytes in %d chunks", got, n)
	}
	// 取消后及时停止。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out2 := make(chan stream.Chunk)
	if err := stream.ReadChunks(ctx, buf, 100, out2); err == nil {
		t.Fatalf("expected cancel error")
	}
}

func TestQueueSendAfterClose(t *testing.T) {
	q := stream.NewQueue[int](2)
	q.Close()
	if err := q.Send(context.Background(), 1); err != stream.ErrClosed {
		t.Fatalf("send after close: %v", err)
	}
}
