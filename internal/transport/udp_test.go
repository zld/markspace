package transport

import (
	"errors"
	"net"
	"testing"
	"time"

	"jbbench/internal/protocol"
)

func dialLoopback(t *testing.T, addr *net.UDPAddr) *net.UDPConn {
	t.Helper()
	c, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestReadTimeoutAndPacket(t *testing.T) {
	l, err := NewListener("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	sender := dialLoopback(t, l.LocalAddr())

	if _, _, err := l.ReadPacket(30 * time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout case: got %v, want ErrTimeout", err)
	}

	payload := make([]byte, 128)
	for i := range payload {
		payload[i] = byte(i)
	}
	if _, err := sender.Write(payload); err != nil {
		t.Fatal(err)
	}
	got, src, err := l.ReadPacket(time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(payload) || src == nil {
		t.Fatalf("packet mismatch: len=%d src=%v", len(got), src)
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("byte %d mismatch", i)
		}
	}
}

func TestTruncatedDatagram(t *testing.T) {
	l, err := NewListener("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	sender := dialLoopback(t, l.LocalAddr())

	oversized := make([]byte, protocol.MaxPacketSize+50)
	if _, err := sender.Write(oversized); err != nil {
		t.Fatal(err)
	}
	_, _, err = l.ReadPacket(time.Second)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("got %v, want ErrTruncated", err)
	}
}

func TestCloseUnblocksRead(t *testing.T) {
	l, err := NewListener("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, e := l.ReadPacket(5 * time.Second)
		done <- e
	}()
	time.Sleep(40 * time.Millisecond)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("blocked read returned %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock ReadPacket")
	}
	// Reads after close and repeated close must stay well-behaved.
	if _, _, err := l.ReadPacket(time.Millisecond); !errors.Is(err, ErrClosed) {
		t.Fatalf("post-close read: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}
}
