package receiver

import (
	"net"
	"testing"
	"time"

	"jitterlab/internal/jitter"
	"jitterlab/internal/protocol"
	"jitterlab/internal/scenario"
)

func testCtl() *jitter.Controller {
	return jitter.NewController(jitter.Config{
		FrameInterval:       20 * time.Millisecond,
		BaseTargetMs:        60,
		MinTargetMs:         20,
		MaxTargetMs:         400,
		JitterMargin:        3,
		WindowSize:          256,
		SampleInterval:      100 * time.Millisecond,
		ConcealThreshold:    6,
		MaxSendRegressionMs: 250,
	})
}

func TestReplayEndToEnd(t *testing.T) {
	evs := scenario.Generate(scenario.Config{
		Frames: 1200, Seed: 41, StreamID: 7,
		LossBurst: 7, ReorderDepth: 9, DuplicateRate: 0.03,
		JitterMs: 18, DriftPPM: 120,
		FrameInterval: 20 * time.Millisecond, PayloadBytes: 160,
	})
	ctl := testCtl()
	Replay(evs, ctl, 0, 500*time.Millisecond)
	m := ctl.M
	if m.DuplicateDropped == 0 {
		t.Fatal("expected duplicate_dropped > 0")
	}
	if m.LateDropped == 0 {
		t.Fatal("expected late_dropped > 0")
	}
	if m.Concealed == 0 || m.MaxConcealRun == 0 {
		t.Fatalf("concealed=%d maxrun=%d", m.Concealed, m.MaxConcealRun)
	}
	r, ok := m.Continuity()
	if !ok || r <= 0 || r >= 1 {
		t.Fatalf("continuity %v ok=%v", r, ok)
	}
	if len(m.Watermarks) < 100 {
		t.Fatalf("watermarks=%d", len(m.Watermarks))
	}
	// Tail playout conceals past the last real frame, so slots >= frames.
	if m.Played+m.Concealed < uint64(1200) || m.Played > uint64(1200) {
		t.Fatalf("played=%d concealed=%d", m.Played, m.Concealed)
	}
}

func TestReplayCountsMalformed(t *testing.T) {
	good := scenario.Generate(scenario.Config{
		Frames: 5, Seed: 1, StreamID: 1, FrameInterval: 20 * time.Millisecond,
	})
	evs := append([]scenario.Event{{At: 0, Data: []byte{0, 1, 2}}}, good...)
	ctl := testCtl()
	Replay(evs, ctl, 0, 200*time.Millisecond)
	if ctl.M.Malformed != 1 {
		t.Fatalf("malformed=%d", ctl.M.Malformed)
	}
	if ctl.M.Played == 0 {
		t.Fatal("bad packet must not stall the stream")
	}
}

func TestListenLocalUDP(t *testing.T) {
	ctl := testCtl()
	done := make(chan error, 1)
	go func() { done <- Listen("127.0.0.1:0", 0, ctl, 0) }()
	if err := <-done; err != nil {
		t.Fatalf("zero-duration listen: %v", err)
	}
}

func TestListenReceivesDatagram(t *testing.T) {
	ctl := testCtl()
	// Bind manually to learn the port, then close and rebind via Listen is
	// racy; instead pick a high port and accept the tiny collision risk.
	addr := "127.0.0.1:39123"
	done := make(chan error, 1)
	go func() { done <- Listen(addr, 300*time.Millisecond, ctl, 0) }()
	time.Sleep(50 * time.Millisecond)
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	pkt := protocol.Encode(protocol.Packet{StreamID: 9, Seq: 0, SendTimeMs: 1, Type: protocol.FrameAudio, Payload: []byte{1}})
	if _, err := conn.Write(pkt); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := conn.Write([]byte{0xde, 0xad}); err != nil { // malformed
		t.Fatalf("write bad: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("listen: %v", err)
	}
	if ctl.M.Received != 1 || ctl.M.Malformed != 1 {
		t.Fatalf("received=%d malformed=%d", ctl.M.Received, ctl.M.Malformed)
	}
}
