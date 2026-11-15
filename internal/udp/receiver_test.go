package udp

import (
	"context"
	"net"
	"testing"
	"time"

	"streamlab/internal/jitter"
	"streamlab/internal/receive"
	"streamlab/internal/wire"
)

func TestRunReceivesOnLoopback(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	pipe := receive.NewPipeline(20, 1, 4,
		jitter.Config{FrameMS: 20, Target: 2, MinLevel: 1, MaxLevel: 8}, 16, 3)

	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), conn, pipe, 400*time.Millisecond) }()

	sender, err := net.Dial("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	for i := 0; i < 10; i++ {
		pkt := wire.Encode(&wire.Packet{StreamID: 1, Seq: uint16(i),
			Timestamp: uint32(i * 20), Samples: []int16{50, 50, 50, 50}})
		if _, err := sender.Write(pkt); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if pipe.Received != 10 {
		t.Fatalf("received %d want 10", pipe.Received)
	}
	if len(pipe.Events) == 0 {
		t.Fatal("no playout events from UDP path")
	}
	// The UDP path shares the same controller: it must have observed
	// arrivals and produced a finite jitter estimate.
	if j := pipe.Controller().JitterMS(); j < 0 {
		t.Fatalf("negative jitter %v", j)
	}
}

func TestRunHonoursContextCancel(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	pipe := receive.NewPipeline(20, 1, 4,
		jitter.Config{FrameMS: 20, Target: 2, MinLevel: 1, MaxLevel: 8}, 16, 3)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	if err := Run(ctx, conn, pipe, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancel did not stop receiver promptly")
	}
}
