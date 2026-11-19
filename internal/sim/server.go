// Package sim runs an in-process mock RTSP/RTP server on the loopback
// interface. It serves a deterministic fixture stream over UDP or
// RTP-over-TCP and can force a server-side disconnect on demand.
package sim

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"

	"rtspdiag/internal/fixture"
)

// Config configures one mock server instance.
type Config struct {
	// Transport selects which transport the client is expected to use. UDP
	// listeners are only opened for the UDP mode.
	Transport Mode
	// Items are the deterministic datagrams to stream once PLAY starts.
	Items []fixture.Item
	// PacketInterval spaces out RTP writes so reorder timeouts can elapse.
	PacketInterval time.Duration
	// DisconnectAfter, when positive, forces a server disconnect after this
	// many RTP packets have been written. Zero disables mid-stream disconnect.
	DisconnectAfter int
	// DisconnectAtEnd closes the control connection after every item was
	// streamed, exercising client cleanup without losing RTP data.
	DisconnectAtEnd bool
}

// Mode selects the RTP transport of the mock server.
type Mode int

const (
	TransportUDP Mode = iota
	TransportTCP
)

// MaxConcurrentSessions bounds how many RTSP sessions the mock server tracks
// at once. Anomalous clients cannot make the server accumulate unbounded
// session state; the limit is small because this is a local single-client tool.
const MaxConcurrentSessions = 8

// Server is a started mock RTSP server.
type Server struct {
	cfg Config

	mu           sync.Mutex
	gortsrv      *gortsplib.Server
	stream       *gortsplib.ServerStream
	media        *description.Media
	clientSS     *gortsplib.ServerSession
	clientC      *gortsplib.ServerConn
	sessions     map[*gortsplib.ServerSession]struct{}
	written      int
	writeErrors  int
	lastWriteErr error
	closed       bool
	rtspAddr     string
}

// handler adapts the gortsplib callback interfaces to *Server.
type handler struct{ s *Server }

// Start builds and starts a loopback-only mock server using ephemeral ports.
func Start(cfg Config) (*Server, error) {
	forma := &format.Generic{
		PayloadTyp: fixture.PayloadType,
		RTPMa:      "rtspdiag/90000",
	}
	if err := forma.Init(); err != nil {
		return nil, fmt.Errorf("sim: format init: %w", err)
	}
	media := &description.Media{
		Type:    "application",
		Formats: []format.Format{forma},
	}
	desc := &description.Session{Medias: []*description.Media{media}}

	s := &Server{
		cfg:      cfg,
		media:    media,
		sessions: make(map[*gortsplib.ServerSession]struct{}),
	}

	// Reserve an ephemeral loopback TCP port, then release it for gortsplib to
	// rebind. The process is purely local; no external interface is exposed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sim: reserve port: %w", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	gs := &gortsplib.Server{
		Handler:                  handler{s},
		RTSPAddress:              addr,
		ReadTimeout:              5 * time.Second,
		WriteTimeout:             5 * time.Second,
		DisableRTCPSenderReports: true,
	}
	var heldConns []net.PacketConn
	if cfg.Transport == TransportUDP {
		rtpConn, rtcpConn, rtpAddr, rtcpAddr, err := reserveUDPPair()
		if err != nil {
			return nil, err
		}
		heldConns = []net.PacketConn{rtpConn, rtcpConn}
		gs.UDPRTPAddress = rtpAddr
		gs.UDPRTCPAddress = rtcpAddr
		// Hand gortsplib the already-bound sockets so the ports it advertises
		// in SETUP are exactly the sockets it sends RTP/RTCP from.
		gs.ListenPacket = reusePacketConns(rtpConn, rtcpConn)
	}
	if err := gs.Start(); err != nil {
		for _, c := range heldConns {
			_ = c.Close()
		}
		return nil, fmt.Errorf("sim: server start: %w", err)
	}
	s.gortsrv = gs
	s.rtspAddr = addr
	s.stream = gortsplib.NewServerStream(gs, desc)
	return s, nil
}

// Address returns the rtsp:// URL clients must connect to.
func (s *Server) Address() string {
	return "rtsp://" + s.rtspAddr + "/diag"
}

// Media exposes the single media description for client setup.
func (s *Server) Media() *description.Media { return s.media }

// reserveUDPPair binds two consecutive free loopback UDP ports (RTP on an
// even port, RTCP on the following odd port) and keeps the sockets open. The
// caller hands them to gortsplib via a ListenPacket hook, so there is no
// release/rebind window and no port mismatch.
func reserveUDPPair() (net.PacketConn, net.PacketConn, string, string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		probe, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, "", "", fmt.Errorf("sim: reserve udp: %w", err)
		}
		rtpPort := probe.LocalAddr().(*net.UDPAddr).Port
		_ = probe.Close()
		if rtpPort%2 != 0 {
			rtpPort++ // gortsplib requires an even RTP port
		}

		rtpConn, err := net.ListenPacket("udp",
			net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", rtpPort)))
		if err != nil {
			continue // port got taken; retry with a fresh random candidate
		}
		rtcpConn, err := net.ListenPacket("udp",
			net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", rtpPort+1)))
		if err != nil {
			_ = rtpConn.Close()
			continue
		}
		return rtpConn, rtcpConn,
			rtpConn.LocalAddr().String(), rtcpConn.LocalAddr().String(), nil
	}
	return nil, nil, "", "", fmt.Errorf("sim: could not reserve consecutive UDP ports")
}

// reusePacketConns returns a ListenPacket that serves the two pre-bound
// sockets for their exact address, and delegates any other address normally.
func reusePacketConns(conns ...net.PacketConn) func(string, string) (net.PacketConn, error) {
	byAddr := make(map[string]net.PacketConn, len(conns))
	for _, c := range conns {
		byAddr[c.LocalAddr().String()] = c
	}
	return func(network, address string) (net.PacketConn, error) {
		if c, ok := byAddr[address]; ok {
			return c, nil
		}
		return net.ListenPacket(network, address)
	}
}

// Written reports how many RTP packets have been sent.
func (s *Server) Written() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written
}

// WriteError reports the first write failure and how many occurred.
func (s *Server) WriteError() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeErrors, s.lastWriteErr
}

// Close releases the stream, server and all bound ports.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	st := s.stream
	gs := s.gortsrv
	s.mu.Unlock()

	if st != nil {
		st.Close()
	}
	if gs != nil {
		gs.Close()
	}
}

// streamLoop writes the fixture packets once a session is playing, then
// optionally forces a disconnect.
func (s *Server) streamLoop(session *gortsplib.ServerSession) {
	go func() {
		ticker := time.NewTicker(s.cfg.PacketInterval)
		defer ticker.Stop()
		for _, it := range s.cfg.Items {
			if it.Kind != fixture.KindRTP {
				continue // malformed items are not sent by the mock itself
			}
			<-ticker.C
			pkt := &rtp.Packet{}
			if err := pkt.Unmarshal(it.Raw); err != nil {
				continue
			}
			if err := s.stream.WritePacketRTP(s.media, pkt); err != nil {
				s.mu.Lock()
				s.writeErrors++
				s.lastWriteErr = err
				s.mu.Unlock()
				return // client went away or write queue full
			}
			s.mu.Lock()
			s.written++
			written := s.written
			s.mu.Unlock()
			if s.cfg.DisconnectAfter > 0 && written >= s.cfg.DisconnectAfter {
				s.forceDisconnect()
				return
			}
		}
		if s.cfg.DisconnectAtEnd {
			// Give in-flight UDP datagrams time to land, then sever the
			// control channel so the client observes a server disconnect.
			time.Sleep(5 * s.cfg.PacketInterval)
			s.forceDisconnect()
		}
	}()
}

func (s *Server) forceDisconnect() {
	s.mu.Lock()
	conn := s.clientC
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// --- gortsplib handler interface implementation ---

func (h handler) OnConnOpen(ctx *gortsplib.ServerHandlerOnConnOpenCtx) {
	h.s.mu.Lock()
	h.s.clientC = ctx.Conn
	h.s.mu.Unlock()
}

func (h handler) OnConnClose(_ *gortsplib.ServerHandlerOnConnCloseCtx) {
	h.s.mu.Lock()
	h.s.clientC = nil
	h.s.clientSS = nil
	h.s.mu.Unlock()
}

func (h handler) OnSessionOpen(ctx *gortsplib.ServerHandlerOnSessionOpenCtx) {
	h.s.mu.Lock()
	h.s.clientSS = ctx.Session
	h.s.sessions[ctx.Session] = struct{}{}
	h.s.mu.Unlock()
}

func (h handler) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	h.s.mu.Lock()
	delete(h.s.sessions, ctx.Session)
	if h.s.clientSS == ctx.Session {
		h.s.clientSS = nil
	}
	h.s.mu.Unlock()
}

// sessionCapacityAvailable reports whether another session can be admitted.
func (s *Server) sessionCapacityAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions) < MaxConcurrentSessions
}

func (h handler) OnDescribe(_ *gortsplib.ServerHandlerOnDescribeCtx,
) (*base.Response, *gortsplib.ServerStream, error) {
	return &base.Response{StatusCode: base.StatusOK}, h.s.stream, nil
}

func (h handler) OnSetup(_ *gortsplib.ServerHandlerOnSetupCtx,
) (*base.Response, *gortsplib.ServerStream, error) {
	if !h.s.sessionCapacityAvailable() {
		// Refuse new sessions past the bound so the server cannot accumulate
		// unbounded state under anomalous or looping clients.
		return &base.Response{
			StatusCode: base.StatusServiceUnavailable,
			Header:     base.Header{},
		}, nil, fmt.Errorf("sim: max concurrent sessions (%d) reached", MaxConcurrentSessions)
	}
	return &base.Response{StatusCode: base.StatusOK}, h.s.stream, nil
}

func (h handler) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx,
) (*base.Response, error) {
	go h.s.streamLoop(ctx.Session)
	return &base.Response{StatusCode: base.StatusOK}, nil
}
