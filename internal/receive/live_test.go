package receive

import (
	"net"
	"testing"
	"time"

	"jbbench/internal/protocol"
	"jbbench/internal/transport"
)

func sendPackets(t *testing.T, addr *net.UDPAddr, count int, skip map[int]bool) {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	buf := make([]byte, protocol.MaxPacketSize)
	frame := protocol.FrameDurationNS()
	for i := 0; i < count; i++ {
		if skip[i] {
			continue
		}
		samples := make([]int16, protocol.SamplesPerFrame)
		pkt := protocol.Packet{
			Version: protocol.Version, Seq: uint16(i), FrameNo: uint32(i),
			SendNS:  uint64(i) * uint64(frame),
			Samples: samples,
		}
		n := protocol.Encode(buf, pkt)
		if _, err := conn.Write(buf[:n]); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRunLiveLoopbackAndCloseReport(t *testing.T) {
	l, err := transport.NewListener("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultLiveConfig()
	cfg.ReadTimeout = 40 * time.Millisecond
	cfg.MaxIdle = 300 * time.Millisecond

	done := make(chan struct{})
	go func() {
		sendPackets(t, l.LocalAddr(), 60, map[int]bool{10: true, 11: true})
		close(done)
	}()

	rep, err := RunLive(l, cfg)
	<-done
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	if rep.Received < 50 {
		t.Fatalf("received = %d, expected most of 60 packets", rep.Received)
	}
	if rep.Played == 0 {
		t.Fatal("no frames played")
	}
	if rep.ConcealedFrames == 0 {
		t.Fatal("two lost frames were not concealed")
	}
	if len(rep.BufferLevelCurve) == 0 {
		t.Fatal("no watermark curve recorded")
	}
	// Listener must be released after the run.
	if _, _, err := l.ReadPacket(time.Millisecond); err == nil {
		t.Fatal("listener still usable after RunLive returned")
	}
}

func TestRunLiveRejectsBadPackets(t *testing.T) {
	l, err := transport.NewListener("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultLiveConfig()
	cfg.ReadTimeout = 30 * time.Millisecond
	cfg.MaxIdle = 150 * time.Millisecond

	go func() {
		conn, _ := net.DialUDP("udp", nil, l.LocalAddr())
		defer conn.Close()
		buf := make([]byte, protocol.MaxPacketSize)
		pkt := protocol.Packet{
			Version: protocol.Version, FrameNo: 1,
			Samples: make([]int16, protocol.SamplesPerFrame),
		}
		n := protocol.Encode(buf, pkt)
		bad := append([]byte(nil), buf[:n]...)
		bad[0] = 99 // unknown version
		_, _ = conn.Write(bad)
		corrupt := append([]byte(nil), buf[:n]...)
		corrupt[protocol.HeaderSize] ^= 0xFF
		_, _ = conn.Write(corrupt)
	}()

	rep, err := RunLive(l, cfg)
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	if rep.BadVersion == 0 || rep.Corrupt == 0 {
		t.Fatalf("bad packet counters: version=%d corrupt=%d",
			rep.BadVersion, rep.Corrupt)
	}
	if rep.Received != 0 {
		t.Fatal("bad packets must not enter the buffer or count as received")
	}
}
