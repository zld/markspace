package sim

import (
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"rtspdiag/internal/fixture"
)

func TestStartBindsLoopbackAndReleasesPort(t *testing.T) {
	items := fixture.Generate(42, fixture.DefaultPlan(4))
	srv, err := Start(Config{
		Transport:      TransportTCP,
		Items:          items,
		PacketInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	addr := srv.Address()
	u, err := url.Parse(addr)
	if err != nil || u.Scheme != "rtsp" {
		t.Fatalf("bad address %q: %v", addr, err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host != "127.0.0.1" || port == "0" {
		t.Fatalf("address must be loopback with concrete port, got %q", u.Host)
	}
	if !strings.HasSuffix(u.Path, "/diag") {
		t.Fatalf("unexpected path: %s", u.Path)
	}

	srv.Close()
	// After Close the TCP port must be releasable again.
	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		t.Fatalf("port not released after Close: %v", err)
	}
	_ = ln.Close()
}

func TestStartUDPServer(t *testing.T) {
	items := fixture.Generate(1, fixture.DefaultPlan(2))
	srv, err := Start(Config{
		Transport:      TransportUDP,
		Items:          items,
		PacketInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("udp start: %v", err)
	}
	if !strings.HasPrefix(srv.Address(), "rtsp://127.0.0.1:") {
		t.Fatalf("udp server address wrong: %s", srv.Address())
	}
	srv.Close()
}

func TestStartDoubleCloseSafe(t *testing.T) {
	items := fixture.Generate(1, fixture.DefaultPlan(2))
	srv, err := Start(Config{
		Transport:      TransportTCP,
		Items:          items,
		PacketInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	srv.Close()
	srv.Close() // must not panic or block
}
