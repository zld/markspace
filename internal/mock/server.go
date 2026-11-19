package mock

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	pionrtp "github.com/pion/rtp"
)

// Stage identifies an RTSP phase, used to inject negotiation failures.
type Stage string

const (
	StageNone     Stage = ""
	StageDescribe Stage = "describe"
	StageSetup    Stage = "setup"
	StagePlay     Stage = "play"
)

// ServerConfig configures the loopback mock camera.
type ServerConfig struct {
	// FrameCount and Fault generate the deterministic schedule.
	FrameCount int
	Fault      FaultConfig
	// FailStage makes the named request return a non-2xx status.
	FailStage Stage
	// FailStatus is the status returned when FailStage is set.
	FailStatus base.StatusCode
	// DropConnAfterPlay closes the TCP connection right after PLAY,
	// simulating a mid-stream connection interruption.
	DropConnAfterPlay bool
}

// Server is a loopback-only mock RTSP source.
type Server struct {
	cfg      ServerConfig
	server   *gortsplib.Server
	listener net.Listener
	desc     *description.Session
	stream   *gortsplib.ServerStream
	mu       sync.Mutex
	conn     *gortsplib.ServerConn

	schedule  []PacketSpec
	done      chan struct{}
	ready     chan struct{}
	sent      chan struct{}
	readyOnce sync.Once
	sentOnce  sync.Once
	rtpPort   int
}

// NewServer creates (but does not start) a mock server. rtpPort and
// rtcpPort are the loopback UDP ports the server uses; pass 0 to let
// the OS choose and read them back via UDPAddresses after Start.
func NewServer(cfg ServerConfig, rtpPort, rtcpPort int) (*Server, error) {
	forma := &format.Generic{
		PayloadTyp: videoPT,
		RTPMa:      fmt.Sprintf("RTSPCHECK/%d", clockTicks),
	}
	if err := forma.Init(); err != nil {
		return nil, fmt.Errorf("mock: format init: %w", err)
	}

	media := &description.Media{
		Type:    description.MediaTypeVideo,
		Control: "trackID=0",
		Formats: []format.Format{forma},
	}
	desc := &description.Session{Medias: []*description.Media{media}}

	// Pre-bind on loopback so the real port is known and can be
	// reported to clients; gortsplib takes ownership through Listen.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("mock: tcp listen: %w", err)
	}

	s := &Server{
		cfg:      cfg,
		desc:     desc,
		listener: ln,
		schedule: GenerateStream(cfg.FrameCount, cfg.Fault),
		done:     make(chan struct{}),
		ready:    make(chan struct{}),
		sent:     make(chan struct{}),
		rtpPort:  rtpPort,
	}

	s.server = &gortsplib.Server{
		Handler:        s,
		RTSPAddress:    ln.Addr().String(),
		UDPRTPAddress:  fmt.Sprintf("127.0.0.1:%d", rtpPort),
		UDPRTCPAddress: fmt.Sprintf("127.0.0.1:%d", rtcpPort),
		// Large enough to absorb a full frame's burst without
		// dropping; pacing in sendLoop keeps it from filling.
		WriteQueueSize: 1024,
		// Hand over the pre-bound loopback listener.
		Listen: func(network, address string) (net.Listener, error) {
			return s.listener, nil
		},
	}
	return s, nil
}

// Start binds the server and prepares the stream.
func (s *Server) Start() error {
	if err := s.server.Start(); err != nil {
		s.listener.Close()
		return fmt.Errorf("mock: server start: %w", err)
	}
	s.stream = gortsplib.NewServerStream(s.server, s.desc)
	return s.stream.Initialize()
}

// RTSPAddress returns the actual loopback host:port of the listener.
func (s *Server) RTSPAddress() string {
	return s.listener.Addr().String()
}

// Description exposes the negotiated session description.
func (s *Server) Description() *description.Session { return s.desc }

// ScheduledPackets is the number of datagrams the send loop will
// attempt to deliver (drops excluded, duplicates included). It is the
// deterministic completion target for the receive side.
func (s *Server) ScheduledPackets() int { return len(s.schedule) }

// Wait blocks until the scheduled stream has been fully sent.
func (s *Server) Wait() { <-s.done }

// Close releases the stream and the server listener.
func (s *Server) Close() {
	s.readyOnce.Do(func() { close(s.ready) })
	if s.stream != nil {
		s.stream.Close()
	}
	s.server.Close()
}

// --- gortsplib handler callbacks ---

// OnConnOpen records the active loopback connection.
func (s *Server) OnConnOpen(ctx *gortsplib.ServerHandlerOnConnOpenCtx) {
	s.mu.Lock()
	s.conn = ctx.Conn
	s.mu.Unlock()
}

// OnConnClose clears the recorded connection.
func (s *Server) OnConnClose(_ *gortsplib.ServerHandlerOnConnCloseCtx) {
	s.mu.Lock()
	s.conn = nil
	s.mu.Unlock()
}

// OnSessionOpen is required by the handler interface.
func (s *Server) OnSessionOpen(_ *gortsplib.ServerHandlerOnSessionOpenCtx) {}

// OnSessionClose is required by the handler interface.
func (s *Server) OnSessionClose(_ *gortsplib.ServerHandlerOnSessionCloseCtx) {}

// OnDescribe returns the description or the injected failure status.
func (s *Server) OnDescribe(
	_ *gortsplib.ServerHandlerOnDescribeCtx,
) (*base.Response, *gortsplib.ServerStream, error) {
	if s.cfg.FailStage == StageDescribe {
		return &base.Response{StatusCode: s.cfg.FailStatus}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

// OnSetup accepts SETUP or returns the injected failure status.
func (s *Server) OnSetup(
	_ *gortsplib.ServerHandlerOnSetupCtx,
) (*base.Response, *gortsplib.ServerStream, error) {
	if s.cfg.FailStage == StageSetup {
		return &base.Response{StatusCode: s.cfg.FailStatus}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

// OnPlay starts the deterministic send loop on success.
func (s *Server) OnPlay(
	ctx *gortsplib.ServerHandlerOnPlayCtx,
) (*base.Response, error) {
	if s.cfg.FailStage == StagePlay {
		return &base.Response{StatusCode: s.cfg.FailStatus}, nil
	}
	if s.cfg.DropConnAfterPlay {
		ctx.Conn.Close()
		return &base.Response{StatusCode: base.StatusOK}, nil
	}
	// The reader is only activated by gortsplib after this handler
	// returns, so unblock the send loop on the next scheduler turn.
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.readyOnce.Do(func() { close(s.ready) })
	}()
	go s.sendLoop(ctx.Session)
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// sendLoop writes every scheduled packet with deterministic jitter.
func (s *Server) sendLoop(sess *gortsplib.ServerSession) {
	finishDone := sync.Once{}
	finishSent := sync.Once{}
	defer finishDone.Do(func() { close(s.done) })
	defer finishSent.Do(func() { close(s.sent) })

	// Wait until the reader has been activated post-PLAY.
	select {
	case <-s.ready:
	case <-time.After(2 * time.Second):
		return
	}

	media := s.desc.Medias[0]
	for _, spec := range s.schedule {
		// Pacing keeps the server write queue from overflowing when
		// the client is still draining the control connection. With
		// configured jitter that delay is used; otherwise a fixed
		// microsecond tick preserves an even, deterministic cadence.
		wait := spec.DelayJitter
		if wait == 0 {
			wait = 50
		}
		time.Sleep(time.Duration(wait) * time.Microsecond)
		var pkt pionrtp.Packet
		if err := pkt.Unmarshal(spec.Data); err != nil {
			continue // skip but never crash the mock
		}
		if err := s.stream.WritePacketRTP(media, &pkt); err != nil {
			// Client went away (TEARDOWN/close): stop quietly.
			return
		}
	}
	_ = sess
}

// Sent returns a channel closed once the full schedule has been
// written to the stream (or the send loop exited early).
func (s *Server) Sent() <-chan struct{} { return s.sent }
