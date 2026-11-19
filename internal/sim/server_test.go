package sim

import (
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"

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

func TestSessionCapacityBound(t *testing.T) {
	items := fixture.Generate(1, fixture.DefaultPlan(2))
	srv, err := Start(Config{
		Transport: TransportTCP, Items: items,
		PacketInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close()

	h := handler{s: srv}
	// Simulate opening sessions up to the bound without real RTSP clients.
	for i := 0; i < MaxConcurrentSessions; i++ {
		ss := &gortsplib.ServerSession{}
		h.OnSessionOpen(&gortsplib.ServerHandlerOnSessionOpenCtx{Session: ss})
		// Capacity remains available until the last allowed slot is taken.
		wantAvailable := i+1 < MaxConcurrentSessions
		if got := srv.sessionCapacityAvailable(); got != wantAvailable {
			t.Fatalf("after %d sessions: available=%d want %v",
				i+1, len(srv.sessions), wantAvailable)
		}
	}

	// At capacity, SETUP must be refused.
	res, _, err := h.OnSetup(nil)
	if err == nil || res.StatusCode != base.StatusServiceUnavailable {
		t.Fatalf("expected 503 at capacity, got res=%v err=%v", res, err)
	}

	// Closing one session frees a slot and SETUP succeeds again.
	var first *gortsplib.ServerSession
	srv.mu.Lock()
	for ss := range srv.sessions {
		first = ss
		break
	}
	srv.mu.Unlock()
	h.OnSessionClose(&gortsplib.ServerHandlerOnSessionCloseCtx{Session: first})
	if !srv.sessionCapacityAvailable() {
		t.Fatal("slot not released after session close")
	}
	res, _, err = h.OnSetup(nil)
	if err != nil || res.StatusCode != base.StatusOK {
		t.Fatalf("expected 200 after slot freed, got res=%v err=%v", res, err)
	}
}
