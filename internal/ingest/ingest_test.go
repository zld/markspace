package ingest

import (
	"context"
	"encoding/binary"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"jitterlab/internal/wire"
)

func testPacket(t *testing.T, seq uint16) []byte {
	t.Helper()
	raw, err := wire.Encode(wire.Packet{Seq: seq, Timestamp: 1, Payload: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestUDPReceiveAndMalformed(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().(*net.UDPAddr)
	var got, bad atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeUDP(ctx, conn, Handler{
			OnPacket:    func(wire.Packet) { got.Add(1) },
			OnMalformed: func() { bad.Add(1) },
		})
	}()
	out, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write(testPacket(t, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("garbage")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for (got.Load() < 1 || bad.Load() < 1) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got.Load() != 1 || bad.Load() != 1 {
		t.Fatalf("got=%d bad=%d", got.Load(), bad.Load())
	}
}

func writeTCPFrame(t *testing.T, c net.Conn, raw []byte) {
	t.Helper()
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(raw)))
	if _, err := c.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(raw); err != nil {
		t.Fatal(err)
	}
}

func TestTCPSplitAndEOF(t *testing.T) {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	var got atomic.Int64
	ctx := context.Background()
	done := make(chan error, 1)
	go func() {
		done <- ServeTCP(ctx, ln, Handler{OnPacket: func(wire.Packet) { got.Add(1) }})
	}()
	c, err := net.DialTCP("tcp", nil, ln.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	// Send two frames byte-by-byte to force split reads.
	for seq := uint16(0); seq < 2; seq++ {
		raw := testPacket(t, seq)
		var hdr [2]byte
		binary.BigEndian.PutUint16(hdr[:], uint16(len(raw)))
		all := append(hdr[:], raw...)
		for _, b := range all {
			if _, err := c.Write([]byte{b}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for got.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	c.Close() // clean EOF
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got.Load() != 2 {
		t.Fatalf("got %d", got.Load())
	}
}

func TestTCPInvalidLengthAndMidFrameEOF(t *testing.T) {
	// Invalid length: server counts malformed and returns an error.
	ln, _ := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	var bad atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- ServeTCP(context.Background(), ln, Handler{
			OnPacket:    func(wire.Packet) {},
			OnMalformed: func() { bad.Add(1) },
		})
	}()
	c, _ := net.DialTCP("tcp", nil, ln.Addr().(*net.TCPAddr))
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], 3) // < HeaderSize
	c.Write(hdr[:])
	if err := <-done; err == nil {
		t.Fatal("expected framing error")
	}
	if bad.Load() != 1 {
		t.Fatalf("bad %d", bad.Load())
	}
	c.Close()

	// Mid-frame EOF: clean shutdown, no error.
	ln2, _ := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	done2 := make(chan error, 1)
	go func() {
		done2 <- ServeTCP(context.Background(), ln2, Handler{OnPacket: func(wire.Packet) {}})
	}()
	c2, _ := net.DialTCP("tcp", nil, ln2.Addr().(*net.TCPAddr))
	raw := testPacket(t, 9)
	binary.BigEndian.PutUint16(hdr[:], uint16(len(raw)))
	c2.Write(hdr[:])
	c2.Write(raw[:len(raw)/2]) // half a payload, then hang up
	c2.Close()
	if err := <-done2; err != nil {
		t.Fatalf("mid-frame EOF should be clean: %v", err)
	}
}
