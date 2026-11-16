package simulator

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/rtp"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
)

// Server is a loopback-only RTSP server replaying a deterministic
// packet list. It supports both UDP and TCP-interleaved transports.
type Server struct {
	srv    *gortsplib.Server
	stream *gortsplib.ServerStream
	media  *description.Media
	port   int

	done     chan struct{}
	closeOnce sync.Once
}

// Port returns the RTSP port bound on 127.0.0.1.
func (s *Server) Port() int { return s.port }

// URL returns the loopback URL of the test stream.
func (s *Server) URL() string {
	return fmt.Sprintf("rtsp://127.0.0.1:%d/stream", s.port)
}

// Close shuts the server down and stops the sender goroutine.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.stream.Close()
		s.srv.Close()
		s.srv.Wait()
	})
}

// Start allocates random loopback ports, builds the stream and starts
// serving. packets are sent once per PLAY session.
func Start(packets []PacketSpec) (*Server, error) {
	rtspPort, err := freeTCPPort()
	if err != nil {
		return nil, err
	}
	rtpPort, rtcpPort, err := freeUDPPair()
	if err != nil {
		return nil, err
	}

	s := &Server{port: rtspPort, done: make(chan struct{})}
	h := &serverHandler{server: s, packets: packets}

	desc := &description.Session{Medias: []*description.Media{{
		Type: description.MediaTypeVideo,
		Formats: []format.Format{&format.H264{
			PayloadTyp:        96,
			SPS:               SPS(),
			PPS:               PPS(),
			PacketizationMode: 1,
		}},
	}}}
	s.media = desc.Medias[0]

	s.srv = &gortsplib.Server{
		Handler:         h,
		RTSPAddress:     fmt.Sprintf("127.0.0.1:%d", rtspPort),
		UDPRTPAddress:   fmt.Sprintf("127.0.0.1:%d", rtpPort),
		UDPRTCPAddress:  fmt.Sprintf("127.0.0.1:%d", rtcpPort),
		WriteTimeout:    5 * time.Second,
		ReadTimeout:     10 * time.Second,
	}
	s.stream = &gortsplib.ServerStream{Server: s.srv, Desc: desc}
	if err := s.stream.Initialize(); err != nil {
		return nil, fmt.Errorf("stream init: %w", err)
	}
	if err := s.srv.Start(); err != nil {
		return nil, fmt.Errorf("server start: %w", err)
	}
	return s, nil
}

// sendAll paces the packet list into the stream until done or closed.
func (s *Server) sendAll(packets []PacketSpec) {
	for _, spec := range packets {
		select {
		case <-s.done:
			return
		case <-time.After(2 * time.Millisecond):
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: spec.Seq,
				Timestamp:      spec.Timestamp,
				SSRC:           spec.SSRC,
				Marker:         spec.Marker,
			},
			Payload: spec.Payload,
		}
		if err := s.stream.WritePacketRTP(s.media, pkt); err != nil {
			return
		}
	}
}

type serverHandler struct {
	server   *Server
	packets  []PacketSpec
	playOnce sync.Once
}

func (h *serverHandler) OnConnClose(ctx *gortsplib.ServerHandlerOnConnCloseCtx) {
	_ = ctx
}

func (h *serverHandler) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	return &base.Response{StatusCode: base.StatusOK}, h.server.stream, nil
}

func (h *serverHandler) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	return &base.Response{StatusCode: base.StatusOK}, h.server.stream, nil
}

func (h *serverHandler) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	h.playOnce.Do(func() {
		go h.server.sendAll(h.packets)
	})
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("alloc tcp port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// freeUDPPair finds an even RTP port with RTP+1 free for RTCP.
func freeUDPPair() (int, int, error) {
	for i := 0; i < 64; i++ {
		c1, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return 0, 0, fmt.Errorf("alloc udp port: %w", err)
		}
		p := c1.LocalAddr().(*net.UDPAddr).Port
		if p%2 != 0 || p+1 > 65535 {
			c1.Close()
			continue
		}
		c2, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p + 1})
		if err != nil {
			c1.Close()
			continue
		}
		c1.Close()
		c2.Close()
		return p, p + 1, nil
	}
	return 0, 0, fmt.Errorf("no free udp port pair on loopback")
}
