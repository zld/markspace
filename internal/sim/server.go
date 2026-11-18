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
	Transport fixtureTransport
	// Items are the deterministic datagrams to stream once PLAY starts.
	Items []fixture.Item
	// PacketInterval spaces out RTP writes so reorder timeouts can elapse.
	PacketInterval time.Duration
	// DisconnectAfter, when positive, forces a server disconnect after this
	// many RTP packets have been written.
	DisconnectAfter int
}

type fixtureTransport int

const (
	TransportUDP fixtureTransport = iota
	TransportTCP
)

// Server is a started mock RTSP server.
type Server struct {
	cfg Config

	mu        sync.Mutex
	gortsrv   *gortsplib.Server
	stream    *gortsplib.ServerStream
	media     *description.Media
	clientSS  *gortsplib.ServerSession
	clientC   *gortsplib.ServerConn
	written   int
	closed    bool
	rtspAddr  string
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

	s := &Server{cfg: cfg, media: media}

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
	if cfg.Transport == TransportUDP {
		rtpAddr, rtcpAddr, err := reserveUDPPair()
		if err != nil {
			return nil, err
		}
		gs.UDPRTPAddress = rtpAddr
		gs.UDPRTCPAddress = rtcpAddr
	}
	if err := gs.Start(); err != nil {
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

// reserveUDPPair finds two consecutive free loopback UDP ports and releases
// them for gortsplib to rebind. gortsplib requires RTP/RTCP ports adjacent.
func reserveUDPPair() (string, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		c1, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return "", "", fmt.Errorf("sim: reserve udp: %w", err)
		}
		addr1 := c1.LocalAddr().(*net.UDPAddr)
		_ = c1.Close()

		port1 := addr1.Port
		c2, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1",
			fmt.Sprintf("%d", port1+1)))
		if err != nil {
			continue // odd port or neighbor taken; retry with a fresh random port
		}
		addr2 := c2.LocalAddr().(*net.UDPAddr)
		_ = c2.Close()
		if addr2.Port != port1+1 {
			continue
		}
		return addr1.String(), addr2.String(), nil
	}
	return "", "", fmt.Errorf("sim: could not reserve consecutive UDP ports")
}

// Written reports how many RTP packets have been sent.
func (s *Server) Written() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written
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
				return // client went away
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
	h.s.mu.Unlock()
}

func (h handler) OnSessionClose(_ *gortsplib.ServerHandlerOnSessionCloseCtx) {
	h.s.mu.Lock()
	h.s.clientSS = nil
	h.s.mu.Unlock()
}

func (h handler) OnDescribe(_ *gortsplib.ServerHandlerOnDescribeCtx,
) (*base.Response, *gortsplib.ServerStream, error) {
	return &base.Response{StatusCode: base.StatusOK}, h.s.stream, nil
}

func (h handler) OnSetup(_ *gortsplib.ServerHandlerOnSetupCtx,
) (*base.Response, *gortsplib.ServerStream, error) {
	return &base.Response{StatusCode: base.StatusOK}, h.s.stream, nil
}

func (h handler) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx,
) (*base.Response, error) {
	go h.s.streamLoop(ctx.Session)
	return &base.Response{StatusCode: base.StatusOK}, nil
}
