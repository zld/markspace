package netem

import (
	"errors"
	"net"
	"testing"
	"time"

	"jitterlab/internal/packet"
	"jitterlab/internal/testkit"
)

func TestLoopbackRoundTrip(t *testing.T) {
	rx, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer rx.Close()
	tx, err := Dial(rx.Port())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()

	want := packet.Encode(packet.Frame{Version: packet.Version, Seq: 7})
	if err := tx.Send(want); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	dg, err := rx.Read(buf, 2*time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(dg.Data) != string(want) {
		t.Fatalf("payload mismatch")
	}
	if _, err := packet.Decode(dg.Data); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestReadTimeout(t *testing.T) {
	rx, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer rx.Close()
	buf := make([]byte, 64)
	_, err = rx.Read(buf, 20*time.Millisecond)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("err = %v, want timeout", err)
	}
}

func TestCloseReleasesSocket(t *testing.T) {
	rx, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	port := rx.Port()
	if err := rx.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rx.Close(); err == nil {
		// A second close returns an error on some platforms; either is fine
		// as long as the socket is gone.
		_ = err
	}
	rx2, err := Listen(port)
	if err != nil {
		t.Fatalf("port %d not released: %v", port, err)
	}
	rx2.Close()
}

func TestBindIsLoopbackOnly(t *testing.T) {
	rx, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer rx.Close()
	addr := rx.conn.LocalAddr().(*net.UDPAddr)
	if !addr.IP.IsLoopback() {
		t.Fatalf("bound to %v, want loopback", addr.IP)
	}
}

func TestInvalidPorts(t *testing.T) {
	if _, err := Listen(-1); err == nil {
		t.Fatal("Listen accepted negative port")
	}
	if _, err := Dial(0); err == nil {
		t.Fatal("Dial accepted port 0")
	}
}

func TestPlayPlanPacesAndCounts(t *testing.T) {
	rx, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer rx.Close()
	tx, err := Dial(rx.Port())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()

	plan, err := testkit.Generate(testkit.Options{
		Frames: 6, FrameMs: 5, Seed: 3, Loss: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	stats, err := PlayPlan(tx, plan, stop)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sent+stats.Lost != 6 {
		t.Fatalf("stats = %+v, want sent+lost = 6", stats)
	}

	buf := make([]byte, 2048)
	for i := 0; i < stats.Sent; i++ {
		dg, err := rx.Read(buf, time.Second)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if _, err := packet.Decode(dg.Data); err != nil {
			t.Fatalf("datagram %d: %v", i, err)
		}
	}
}

func TestPlayPlanStopsEarly(t *testing.T) {
	rx, _ := Listen(0)
	defer rx.Close()
	tx, err := Dial(rx.Port())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()

	plan, _ := testkit.Generate(testkit.Options{Frames: 100, FrameMs: 20, Seed: 1})
	stop := make(chan struct{})
	close(stop)
	stats, err := PlayPlan(tx, plan, stop)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sent >= 100 {
		t.Fatalf("sent %d events after stop", stats.Sent)
	}
}
