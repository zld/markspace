package jitter

import (
	"testing"
	"time"

	"jitterlab/internal/protocol"
)

func testConfig() Config {
	return Config{
		FrameInterval:       20 * time.Millisecond,
		BaseTargetMs:        60,
		MinTargetMs:         20,
		MaxTargetMs:         400,
		JitterMargin:        3,
		WindowSize:          64,
		SampleInterval:      100 * time.Millisecond,
		ConcealThreshold:    3,
		MaxSendRegressionMs: 250,
	}
}

func apkt(stream, seq uint32, sendMs int64) protocol.Packet {
	return protocol.Packet{StreamID: stream, Seq: seq, SendTimeMs: sendMs,
		Type: protocol.FrameAudio, Payload: []byte{byte(seq)}}
}

func TestControllerConcealsAndCountsLate(t *testing.T) {
	c := NewController(testConfig())
	var outs []Output
	c.OnOutput = func(o Output) { outs = append(outs, o) }

	// Frames 0 and 1 arrive; frame 2 never does; frame 3 arrives late.
	c.AddPacket(apkt(1, 0, 1000), 1040*time.Millisecond)
	c.AddPacket(apkt(1, 1, 1020), 1060*time.Millisecond)
	c.PlayoutUntil(1130 * time.Millisecond) // play 0..1 (deadlines 1100, 1120)
	if c.M.Played != 2 || c.M.Concealed != 0 {
		t.Fatalf("played=%d concealed=%d", c.M.Played, c.M.Concealed)
	}
	c.PlayoutUntil(1150 * time.Millisecond) // deadline of seq 2 (1140) passes
	if c.M.Concealed != 1 {
		t.Fatalf("concealed=%d", c.M.Concealed)
	}
	// Late arrival of seq 2 must not overwrite the concealed output.
	c.AddPacket(apkt(1, 2, 1040), 1185*time.Millisecond)
	if c.M.LateDropped != 1 {
		t.Fatalf("late=%d", c.M.LateDropped)
	}
	if outs[2].Frame.Seq != 2 || !outs[2].Frame.Concealed {
		t.Fatalf("slot 2 overwritten: %+v", outs[2].Frame)
	}
}

func TestControllerUnrecoverableRun(t *testing.T) {
	c := NewController(testConfig())
	c.AddPacket(apkt(1, 0, 1000), 1040*time.Millisecond)
	// 4 missing frames with threshold 3 -> last two unrecoverable.
	c.PlayoutUntil(1040*time.Millisecond + 60*time.Millisecond + 4*20*time.Millisecond)
	if c.M.MaxConcealRun != 4 {
		t.Fatalf("max run=%d", c.M.MaxConcealRun)
	}
	if c.M.Unrecoverable != 2 {
		t.Fatalf("unrecoverable=%d", c.M.Unrecoverable)
	}
}

func TestControllerRejectsSendTimeRegression(t *testing.T) {
	c := NewController(testConfig())
	c.AddPacket(apkt(1, 0, 1000), 1040*time.Millisecond)
	c.AddPacket(apkt(1, 1, 100), 1060*time.Millisecond) // 900ms backtrack
	if c.M.Malformed != 1 || c.M.Received != 1 {
		t.Fatalf("malformed=%d received=%d", c.M.Malformed, c.M.Received)
	}
	// Reorder-scale backtrack (within tolerance) is accepted.
	c.AddPacket(apkt(1, 2, 980), 1080*time.Millisecond)
	if c.M.Received != 2 {
		t.Fatalf("received=%d", c.M.Received)
	}
}

func TestControllerStreamIsolation(t *testing.T) {
	c := NewController(testConfig())
	c.AddPacket(apkt(1, 0, 1000), 1040*time.Millisecond)
	c.AddPacket(apkt(1, 1, 0), 1060*time.Millisecond) // malformed on stream 1
	c.AddPacket(apkt(2, 0, 1000), 1040*time.Millisecond) // stream 2 unaffected
	c.PlayoutUntil(1200 * time.Millisecond)
	if c.M.Malformed != 1 {
		t.Fatalf("malformed=%d", c.M.Malformed)
	}
	if c.M.Played != 2 { // one played frame per stream
		t.Fatalf("played=%d", c.M.Played)
	}
}

func TestControllerDrainFlushesTail(t *testing.T) {
	c := NewController(testConfig())
	c.AddPacket(apkt(1, 0, 1000), 1040*time.Millisecond)
	c.AddPacket(apkt(1, 2, 1040), 1080*time.Millisecond) // seq 1 lost
	c.PlayoutUntil(1100 * time.Millisecond)            // play seq 0 only
	c.Drain()                                          // conceal 1, play 2
	if c.M.Played != 2 || c.M.Concealed != 1 {
		t.Fatalf("played=%d concealed=%d", c.M.Played, c.M.Concealed)
	}
}

func TestControllerWatermarks(t *testing.T) {
	c := NewController(testConfig())
	for i := 0; i < 10; i++ {
		c.AddPacket(apkt(1, uint32(i), 1000+int64(i)*20),
			time.Duration(1040+i*20)*time.Millisecond)
	}
	c.PlayoutUntil(1040*time.Millisecond + 60*time.Millisecond + 10*20*time.Millisecond)
	// 10 slots at 20ms with a 100ms sample interval -> at least 2 samples.
	if len(c.M.Watermarks) < 2 {
		t.Fatalf("watermarks=%d", len(c.M.Watermarks))
	}
	if _, ok := c.M.Continuity(); !ok {
		t.Fatal("continuity must be defined after output")
	}
}
