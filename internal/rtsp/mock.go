// Package rtsp drives the gortsplib client and hosts an in-process mock
// RTSP server bound exclusively to the loopback interface.
package rtsp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"

	"rtspcheck/internal/demo"
)

// loopbackHost is the only address the mock ever binds or accepts.
const loopbackHost = "127.0.0.1"

// drainDelay lets in-flight UDP datagrams arrive before sessions close.
const drainDelay = 100 * time.Millisecond

// FaultMode selects server-side failure behaviour used by cleanup tests.
type FaultMode int

const (
	FaultNone FaultMode = iota
	// FaultBadSDP makes DESCRIBE return an invalid SDP body.
	FaultBadSDP
	// FaultSetupRejected makes SETUP return an error status.
	FaultSetupRejected
)

// MockServer is a deterministic, loopback-only RTSP server serving one
// H.264 media with a dynamic payload type.
type MockServer struct {
	fault FaultMode
	pace  time.Duration

	mu      sync.Mutex
	server  *gortsplib.Server
	stream  *gortsplib.ServerStream
	desc    *description.Session
	media   *description.Media
	started chan struct{}

	// session bookkeeping
	sessionWG     sync.WaitGroup
	playSessions  map[*gortsplib.ServerSession]struct{}
	playMu        sync.Mutex
	requests      int
}

// handler adapts the MockServer to gortsplib's callback handler set.
type handler struct{ m *MockServer }

// NewMockServer creates a server. It is not listening until Start runs.
func NewMockServer(fault FaultMode, pace time.Duration) *MockServer {
	return &MockServer{
		fault:        fault,
		pace:         pace,
		started:      make(chan struct{}),
		playSessions: make(map[*gortsplib.ServerSession]struct{}),
		desc: &description.Session{
			Medias: []*description.Media{{
				Type: description.MediaTypeVideo,
				Formats: []format.Format{&format.H264{
					PayloadTyp:        demo.PayloadType,
					PacketizationMode: 1,
				}},
			}},
		},
	}
}

// Start binds the RTSP listener and UDP ports on 127.0.0.1 using ephemeral
// ports. Binding any non-loopback address is refused up front.
func (m *MockServer) Start() error {
	rtspPort, err := freeLoopbackPort()
	if err != nil {
		return err
	}
	rtpPort, rtcpPort, err := freeLoopbackPortPair()
	if err != nil {
		return err
	}

	h := &handler{m: m}
	srv := &gortsplib.Server{
		Handler:        h,
		RTSPAddress:    net.JoinHostPort(loopbackHost, fmt.Sprintf("%d", rtspPort)),
		UDPRTPAddress:  net.JoinHostPort(loopbackHost, fmt.Sprintf("%d", rtpPort)),
		UDPRTCPAddress: net.JoinHostPort(loopbackHost, fmt.Sprintf("%d", rtcpPort)),
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   5 * time.Second,
		Listen: func(network, address string) (net.Listener, error) {
			return listenLoopback(network, address)
		},
		ListenPacket: func(network, address string) (net.PacketConn, error) {
			return listenPacketLoopback(network, address)
		},
	}
	if err := srv.Start(); err != nil {
		return fmt.Errorf("mock server start: %w", err)
	}

	st := &gortsplib.ServerStream{Server: srv, Desc: m.desc}
	if err := st.Initialize(); err != nil {
		srv.Close()
		return fmt.Errorf("mock stream init: %w", err)
	}

	m.mu.Lock()
	m.server = srv
	m.stream = st
	m.media = m.desc.Medias[0]
	m.mu.Unlock()
	close(m.started)
	return nil
}

// URL returns the rtsp:// URL clients must use. Only loopback URLs work.
func (m *MockServer) URL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf("rtsp://%s/demo", m.server.RTSPAddress)
}

// Close releases the stream, sessions and listeners. It is safe to call
// multiple times.
func (m *MockServer) Close() {
	m.mu.Lock()
	srv := m.server
	st := m.stream
	m.server = nil
	m.stream = nil
	m.mu.Unlock()

	if st != nil {
		st.Close()
	}
	if srv != nil {
		srv.Close()
	}
	m.sessionWG.Wait()
}

// Stream sends one packet list to every active PLAY session. It returns
// when all sends have been attempted or ctx is canceled.
func (m *MockServer) Stream(ctx context.Context, manifest *demo.Manifest) error {
	<-m.started
	if err := m.waitForPlayer(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(m.pace)
	defer ticker.Stop()

	for i, p := range manifest.Packets {
		// The first packet goes out immediately after PLAY so the client's
		// initial UDP read timeout cannot expire before data starts; later
		// packets keep the configured pacing.
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
		m.mu.Lock()
		st := m.stream
		medi := m.media
		m.mu.Unlock()
		if st == nil {
			return fmt.Errorf("mock server closed mid-stream")
		}
		// WritePacketRTP fans out to all readers; a disconnected reader is
		// pruned by gortsplib and must not stop the run.
		_ = st.WritePacketRTP(medi, p.Packet)
	}

	// Let the last datagrams drain, then close the PLAY sessions so the
	// client's Wait() returns instead of blocking until its read timeout.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(drainDelay):
	}
	m.closePlayers()
	return nil
}

// closePlayers terminates every active PLAY session. It drives the client
// through an orderly connection close rather than a timeout.
func (m *MockServer) closePlayers() {
	m.playMu.Lock()
	sessions := make([]*gortsplib.ServerSession, 0, len(m.playSessions))
	for s := range m.playSessions {
		sessions = append(sessions, s)
	}
	m.playMu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
}

// waitForPlayer blocks until at least one session entered PLAY or ctx ends.
func (m *MockServer) waitForPlayer(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		m.playMu.Lock()
		n := len(m.playSessions)
		m.playMu.Unlock()
		if n > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *MockServer) recordRequest() {
	m.mu.Lock()
	m.requests++
	m.mu.Unlock()
}

// ---- gortsplib handler callbacks ----

func (h *handler) OnConnOpen(_ *gortsplib.ServerHandlerOnConnOpenCtx) {}

func (h *handler) OnConnClose(_ *gortsplib.ServerHandlerOnConnCloseCtx) {}

func (h *handler) OnSessionOpen(_ *gortsplib.ServerHandlerOnSessionOpenCtx) {
	h.m.sessionWG.Add(1)
}

func (h *handler) OnSessionClose(c *gortsplib.ServerHandlerOnSessionCloseCtx) {
	h.m.playMu.Lock()
	delete(h.m.playSessions, c.Session)
	h.m.playMu.Unlock()
	h.m.sessionWG.Done()
}

func (h *handler) OnDescribe(c *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	h.m.recordRequest()
	if h.m.fault == FaultBadSDP {
		return &base.Response{
			StatusCode: base.StatusOK,
			Header:     base.Header{"Content-Type": []string{"application/sdp"}},
			Body:       []byte("this is not a valid sdp body"),
		}, nil, nil
	}
	h.m.mu.Lock()
	st := h.m.stream
	h.m.mu.Unlock()
	return &base.Response{StatusCode: base.StatusOK}, st, nil
}

func (h *handler) OnSetup(c *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	h.m.recordRequest()
	if h.m.fault == FaultSetupRejected {
		return &base.Response{StatusCode: base.StatusUnsupportedTransport}, nil, nil
	}
	h.m.mu.Lock()
	st := h.m.stream
	h.m.mu.Unlock()
	return &base.Response{StatusCode: base.StatusOK}, st, nil
}

func (h *handler) OnPlay(c *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	h.m.recordRequest()
	h.m.playMu.Lock()
	h.m.playSessions[c.Session] = struct{}{}
	h.m.playMu.Unlock()
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// freeLoopbackPort asks the kernel for a free loopback TCP port.
func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(loopbackHost, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// freeLoopbackPortPair returns a free, consecutive (even, odd) UDP port
// pair on loopback, as required by the RTP/RTCP transport rule. Both
// sockets are closed before returning, so a retry loop handles the rare
// race where another process grabs the ports first.
func freeLoopbackPortPair() (rtpPort, rtcpPort int, err error) {
	for attempt := 0; attempt < 16; attempt++ {
		c, err := net.ListenPacket("udp", net.JoinHostPort(loopbackHost, "0"))
		if err != nil {
			return 0, 0, err
		}
		port := c.LocalAddr().(*net.UDPAddr).Port
		c.Close()

		if port%2 != 0 {
			port--
		}
		rtpAddr := net.JoinHostPort(loopbackHost, fmt.Sprintf("%d", port))
		rtcpAddr := net.JoinHostPort(loopbackHost, fmt.Sprintf("%d", port+1))

		rc, errR := net.ListenPacket("udp", rtpAddr)
		if errR != nil {
			continue
		}
		cc, errC := net.ListenPacket("udp", rtcpAddr)
		if errC != nil {
			rc.Close()
			continue
		}
		rc.Close()
		cc.Close()
		return port, port + 1, nil
	}
	return 0, 0, fmt.Errorf("could not allocate a free RTP/RTCP port pair")
}

func listenLoopback(network, address string) (net.Listener, error) {
	if err := assertLoopback(address); err != nil {
		return nil, err
	}
	return net.Listen(network, address)
}

func listenPacketLoopback(network, address string) (net.PacketConn, error) {
	if err := assertLoopback(address); err != nil {
		return nil, err
	}
	return net.ListenPacket(network, address)
}

// assertLoopback refuses any address that is not bound to 127.0.0.1.
func assertLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listener address %q: %w", address, err)
	}
	if host != loopbackHost {
		return fmt.Errorf("refusing to bind non-loopback address %q", address)
	}
	return nil
}
