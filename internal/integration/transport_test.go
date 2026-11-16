// Package integration exercises the full audit pipeline against the
// local simulator over both UDP and TCP-interleaved transports.
package integration

import (
	"context"
	"errors"
	"net"
	"runtime"
	"testing"
	"time"

	"rtsp-audit/internal/rtsp"
	"rtsp-audit/internal/simulator"
)

func runSession(t *testing.T, mode rtsp.TransportMode, scenario string, seed int64) *rtsp.Stats {
	t.Helper()
	srv, err := simulator.Start(simulator.Generate(scenario, seed))
	if err != nil {
		t.Fatalf("simulator start: %v", err)
	}
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stats, err := rtsp.Run(ctx, srv.URL(), mode, 300*time.Millisecond, simulator.IsPaddingPayload)
	if err != nil {
		t.Fatalf("run %s/%s: %v", mode, scenario, err)
	}
	return stats
}

func checkStressStats(t *testing.T, s *rtsp.Stats) {
	t.Helper()
	if s.Transitions != 6 {
		t.Errorf("transitions = %d, want 6", s.Transitions)
	}
	if s.RTP.Gaps != 1 {
		t.Errorf("seq gaps = %d, want 1", s.RTP.Gaps)
	}
	if s.H264.Frames != 6 {
		t.Errorf("frames = %d, want 6", s.H264.Frames)
	}
	if s.H264.DroppedFrames != 1 {
		t.Errorf("dropped frames = %d, want 1", s.H264.DroppedFrames)
	}
	if s.RTP.Received != 11 {
		t.Errorf("received = %d, want 11", s.RTP.Received)
	}
	if s.RTP.Wraps != 1 {
		t.Errorf("wraparounds = %d, want 1", s.RTP.Wraps)
	}
}

func TestStressUDP(t *testing.T) {
	checkStressStats(t, runSession(t, rtsp.TransportUDP, "stress", 41))
}

func TestStressTCPInterleaved(t *testing.T) {
	s := runSession(t, rtsp.TransportTCP, "stress", 41)
	checkStressStats(t, s)
	if s.RTP.Duplicates != 1 {
		t.Errorf("duplicates = %d, want 1", s.RTP.Duplicates)
	}
	if s.RTP.Reordered == 0 {
		t.Errorf("reordered = 0, want > 0")
	}
}

func TestStressReproducible(t *testing.T) {
	a := runSession(t, rtsp.TransportUDP, "stress", 41)
	b := runSession(t, rtsp.TransportUDP, "stress", 41)
	if *a != *b {
		t.Errorf("same seed produced different stats:\n%+v\n%+v", a, b)
	}
}

func TestNormalScenario(t *testing.T) {
	s := runSession(t, rtsp.TransportTCP, "normal", 7)
	if s.Transitions != 6 || s.RTP.Gaps != 0 || s.H264.Frames != 7 ||
		s.H264.DroppedFrames != 0 || s.RTP.Duplicates != 0 {
		t.Errorf("unexpected normal stats: %+v", s)
	}
}

func TestExternalURLRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := rtsp.Run(ctx, "rtsp://203.0.113.1:8554/x", rtsp.TransportTCP, time.Second, nil)
	if !errors.Is(err, rtsp.ErrExternalURL) {
		t.Errorf("err = %v, want ErrExternalURL", err)
	}
}

func TestEarlyDisconnect(t *testing.T) {
	// a server that accepts and immediately closes the connection
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stats, err := rtsp.Run(ctx, "rtsp://"+l.Addr().String()+"/x", rtsp.TransportTCP, 200*time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected protocol error on early disconnect")
	}
	if stats == nil {
		t.Fatal("partial stats must still be returned")
	}
}

func TestRTSPTimeout(t *testing.T) {
	// a server that accepts but never answers
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	stats, err := rtsp.Run(ctx, "rtsp://"+l.Addr().String()+"/x", rtsp.TransportTCP, 200*time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if stats == nil || stats.Transitions != 0 {
		t.Errorf("stats = %+v, want zero transitions", stats)
	}
}

func TestNoListenerLeak(t *testing.T) {
	srv, err := simulator.Start(simulator.Generate("stress", 1))
	if err != nil {
		t.Fatal(err)
	}
	url := srv.URL()
	srv.Close()
	// the RTSP port must be closed immediately after Close
	conn, err := net.DialTimeout("tcp", url[len("rtsp://"):len(url)-len("/stream")], 300*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("RTSP port still listening after Close")
	}
}

func TestNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	runSession(t, rtsp.TransportTCP, "stress", 3)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("goroutines before=%d after=%d", before, runtime.NumGoroutine())
}
