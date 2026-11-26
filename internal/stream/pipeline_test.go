package stream_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/pion/rtp"

	"mediaprep/internal/stream"
)

func TestPipelineAppliesBoundsAndBackpressure(t *testing.T) {
	packets := rtpSamplePackets(t, 32)
	var source []byte
	for _, pkt := range packets {
		source = append(source, pkt.Payload...)
	}
	// Repeat the in-memory RTP-generated samples until several fixed blocks.
	base := source
	for len(source) < 6*stream.BlockSize {
		source = append(source, base...)
	}

	pipe, err := stream.New(stream.Options{QueueLength: 2, BufferBudgetBytes: 4 * stream.BlockSize})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pipe.Process(context.Background(), bytes.NewReader(source), func(context.Context, stream.Block) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m := pipe.Snapshot()
	if m.QueuePeak > 2 {
		t.Fatalf("queue peak %d exceeds capacity 2", m.QueuePeak)
	}
	if m.BufferPeakBytes > m.BufferBudgetBytes {
		t.Fatalf("buffer peak %d exceeds budget %d", m.BufferPeakBytes, m.BufferBudgetBytes)
	}
	if m.BlocksProduced == 0 || m.BlocksConsumed != m.BlocksProduced || m.ResidualTasks != 0 {
		t.Fatalf("bad completion metrics: %+v", m)
	}
	if m.BufferActualBytes != 0 {
		t.Fatalf("buffer not released: actual=%d", m.BufferActualBytes)
	}
}

func TestPipelineSlowConsumerTriggersBackpressure(t *testing.T) {
	pipe, err := stream.New(stream.Options{QueueLength: 1, BufferBudgetBytes: 4 * stream.BlockSize})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	sink := func(ctx context.Context, _ stream.Block) error {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := pipe.Process(context.Background(), bytes.NewReader(make([]byte, 5*stream.BlockSize)), sink)
		done <- err
	}()
	<-started
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && pipe.Snapshot().BackpressureEvents == 0 {
		time.Sleep(time.Millisecond)
	}
	if got := pipe.Snapshot().BackpressureEvents; got == 0 {
		close(release)
		t.Fatal("slow consumer did not observe backpressure")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not finish after releasing consumer")
	}
	m := pipe.Snapshot()
	if m.QueuePeak > 1 || m.BufferPeakBytes > 4*stream.BlockSize || m.ResidualTasks != 0 {
		t.Fatalf("bad bounded metrics: %+v", m)
	}
}

func TestPipelineCancelUnblocksAndDrains(t *testing.T) {
	pipe, err := stream.New(stream.Options{QueueLength: 1, BufferBudgetBytes: 2 * stream.BlockSize})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	sink := func(ctx context.Context, _ stream.Block) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := pipe.Process(ctx, bytes.NewReader(make([]byte, 16*stream.BlockSize)), sink)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not release blocked pipeline work")
	}
	m := pipe.Snapshot()
	if m.ResidualTasks != 0 {
		t.Fatalf("residual tasks=%d", m.ResidualTasks)
	}
	if m.BufferActualBytes != 0 {
		t.Fatalf("buffer slots remain held: %d", m.BufferActualBytes)
	}
}

func rtpSamplePackets(t *testing.T, n int) []rtp.Packet {
	t.Helper()
	out := make([]rtp.Packet, 0, n)
	for i := 0; i < n; i++ {
		payload := make([]byte, 1024)
		for j := range payload {
			payload[j] = byte(i*31 + j)
		}
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96, SequenceNumber: uint16(i),
				Timestamp: uint32(i * 960), SSRC: 0x4d505245,
			},
			Payload: payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var decoded rtp.Packet
		if err := decoded.Unmarshal(raw); err != nil {
			t.Fatal(err)
		}
		out = append(out, decoded)
	}
	return out
}

var _ io.Reader = (*bytes.Reader)(nil)
