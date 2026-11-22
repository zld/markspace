// Package sim runs an in-process RTSP server bound to the loopback
// interface. It serves a single deterministic, code-generated RTP
// stream and can be configured to fail one RTSP method so the client
// failure paths can be exercised without any external dependency.
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
)

// Config configures the simulated server.
type Config struct {
	// Packets are sent in order, spaced by PacketInterval, once PLAY
	// has been accepted.
	Packets []*rtp.Packet
	// PacketInterval spaces packet writes so UDP/TCP delivery is
	// observable deterministically. Zero means a small fixed default.
	PacketInterval time.Duration
	// FailMethod optionally names an RTSP method that must return a
	// 500 response (DESCRIBE/SETUP/PLAY). TEARDOWN failure is emulated
	// by severing the session connection mid-stream. Empty disables it.
	FailMethod string
}

// Server is the in-process simulated RTSP server.
type Server struct {
	cfg       Config
	server    *gortsplib.Server
	h         *handler
	rtspAddr  string

	mu        sync.Mutex
	stream    *gortsplib.ServerStream
	media     *description.Media
	stopWrite chan struct{}
	writeWG   sync.WaitGroup
}

type handler struct {
	srv *Server
}

// freeLoopbackPort reserves a TCP port on 127.0.0.1 by briefly
// listening on it. The listener is closed before returning, so the
// RTSP server can bind the same port immediately afterwards. On a
// loopback-only local process there is no competing listener.
func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// freeUDPPortPair reserves two consecutive UDP ports on 127.0.0.1,
// which gortsplib requires for the RTP/RTCP server listeners. Both
// sockets are closed before returning so the server can rebind them.
func freeUDPPortPair() (int, int, error) {
	for attempt := 0; attempt < 50; attempt++ {
		rtpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return 0, 0, err
		}
		rtpPort := rtpConn.LocalAddr().(*net.UDPAddr).Port
		rtpConn.Close()
		if rtpPort%2 != 0 {
			continue // RTP port must be even so RTCP can be rtp+1
		}
		rtcpConn, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", rtpPort+1))
		if err != nil {
			continue // pair taken between the two probes; retry
		}
		rtcpConn.Close()
		return rtpPort, rtpPort + 1, nil
	}
	return 0, 0, fmt.Errorf("could not reserve consecutive UDP ports")
}

// New creates a server bound to an ephemeral port on 127.0.0.1.
// Call Start to begin accepting connections and Close to release it.
func New(cfg Config) (*Server, error) {
	if cfg.PacketInterval <= 0 {
		cfg.PacketInterval = 5 * time.Millisecond
	}
	port, err := freeLoopbackPort()
	if err != nil {
		return nil, fmt.Errorf("reserve loopback port: %w", err)
	}
	rtpPort, rtcpPort, err := freeUDPPortPair()
	if err != nil {
		return nil, fmt.Errorf("reserve UDP port pair: %w", err)
	}
	s := &Server{
		cfg:       cfg,
		rtspAddr:  fmt.Sprintf("127.0.0.1:%d", port),
		stopWrite: make(chan struct{}),
	}
	s.h = &handler{srv: s}
	s.media = &description.Media{
		Type:    description.MediaTypeVideo,
		Control: "trackID=0",
		Formats: []format.Format{&format.Generic{
			PayloadTyp: 96,
			RTPMa:      "rtp/90000",
			ClockRat:   90000,
		}},
	}
	s.server = &gortsplib.Server{
		Handler:        s.h,
		RTSPAddress:    s.rtspAddr,
		UDPRTPAddress:  fmt.Sprintf("127.0.0.1:%d", rtpPort),
		UDPRTCPAddress: fmt.Sprintf("127.0.0.1:%d", rtcpPort),
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   5 * time.Second,
		// RTCP sender reports are irrelevant to the validation and
		// only add nondeterministic traffic.
		DisableRTCPSenderReports: true,
	}
	return s, nil
}

// Start listens on the loopback address.
func (s *Server) Start() error {
	return s.server.Start()
}

// URL returns the rtsp:// URL of the single stream. Only available
// after Start.
func (s *Server) URL() string {
	return fmt.Sprintf("rtsp://%s/stream", s.rtspAddr)
}

// Close stops packet writing and releases the server. It waits for
// any PLAY writer to exit and is safe to call even if PLAY never ran.
func (s *Server) Close() {
	close(s.stopWrite)
	s.writeWG.Wait()
	s.mu.Lock()
	if s.stream != nil {
		s.stream.Close()
		s.stream = nil
	}
	s.mu.Unlock()
	s.server.Close()
}

func (s *Server) methodFailed(method string) bool {
	return s.cfg.FailMethod != "" && s.cfg.FailMethod == method
}

func (s *Server) onDescribe(*gortsplib.ServerHandlerOnDescribeCtx,
) (*base.Response, *gortsplib.ServerStream, error) {
	if s.methodFailed("DESCRIBE") {
		return &base.Response{StatusCode: base.StatusInternalServerError}, nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream == nil {
		desc := &description.Session{Medias: []*description.Media{s.media}}
		s.stream = &gortsplib.ServerStream{Server: s.server, Desc: desc}
		if err := s.stream.Initialize(); err != nil {
			return nil, nil, err
		}
	}
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *Server) onSetup(ctx *gortsplib.ServerHandlerOnSetupCtx,
) (*base.Response, *gortsplib.ServerStream, error) {
	if s.methodFailed("SETUP") {
		return &base.Response{StatusCode: base.StatusInternalServerError}, nil, nil
	}
	s.mu.Lock()
	st := s.stream
	s.mu.Unlock()
	if st == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, st, nil
}

func (s *Server) onPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	if s.methodFailed("PLAY") {
		return &base.Response{StatusCode: base.StatusInternalServerError}, nil
	}
	s.writeWG.Add(1)
	go s.writePackets(ctx.Session)
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// writePackets emits the deterministic packet stream once per PLAY.
// A FailMethod of TEARDOWN severs the connection mid-stream instead.
func (s *Server) writePackets(sess *gortsplib.ServerSession) {
	defer s.writeWG.Done()
	for i, p := range s.cfg.Packets {
		if s.methodFailed("TEARDOWN") && i == len(s.cfg.Packets)/2 {
			sess.Close()
			return
		}
		select {
		case <-s.stopWrite:
			return
		case <-time.After(s.cfg.PacketInterval):
		}
		s.mu.Lock()
		st := s.stream
		s.mu.Unlock()
		if st == nil {
			return
		}
		if err := st.WritePacketRTP(s.media, p); err != nil {
			return
		}
	}
}

// --- gortsplib handler interface implementation ---

func (h *handler) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx,
) (*base.Response, *gortsplib.ServerStream, error) {
	return h.srv.onDescribe(ctx)
}

func (h *handler) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx,
) (*base.Response, *gortsplib.ServerStream, error) {
	return h.srv.onSetup(ctx)
}

func (h *handler) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return h.srv.onPlay(ctx)
}
