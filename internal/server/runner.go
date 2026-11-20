package server

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/bluenviron/gortsplib/v4"

	"rtspingest/internal/fsm"
	"rtspingest/internal/transport"
)

const loopback = "127.0.0.1"

// Runner owns one gortsplib server, its handler and the bound loopback
// ports. A runner serves a single publisher session.
type Runner struct {
	server  *gortsplib.Server
	handler *Handler

	rtspAddr string
	rtpPort  int
	rtcpPort int
}

// Config selects the media transport and the server timeouts.
type Config struct {
	// UseUDP enables the UDP RTP/RTCP listeners. When false only the TCP
	// interleaved path is available.
	UseUDP       bool
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// NewRunner allocates ephemeral loopback ports and starts the RTSP server.
func NewRunner(machine *fsm.Machine, adapter transport.Adapter, cfg Config) (*Runner, error) {
	h := NewHandler(machine, adapter)
	s := &gortsplib.Server{
		Handler:      h,
		RTSPAddress:  "",
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	r := &Runner{server: s, handler: h}

	// Pre-bind the RTSP TCP socket so the chosen ephemeral port is known and
	// the same socket is handed to gortsplib through the Listen hook.
	tcpLn, err := net.Listen("tcp4", loopback+":0")
	if err != nil {
		return nil, fmt.Errorf("server: tcp listen: %w", err)
	}
	r.rtspAddr = tcpLn.Addr().String()
	s.RTSPAddress = r.rtspAddr
	s.Listen = func(network, address string) (net.Listener, error) {
		return tcpLn, nil
	}

	if cfg.UseUDP {
		rtpConn, rtcpConn, rtpPort, rtcpPort, err := allocateUDPPair()
		if err != nil {
			_ = tcpLn.Close()
			return nil, err
		}
		r.rtpPort, r.rtcpPort = rtpPort, rtcpPort
		s.UDPRTPAddress = fmt.Sprintf("%s:%d", loopback, rtpPort)
		s.UDPRTCPAddress = fmt.Sprintf("%s:%d", loopback, rtcpPort)
		s.ListenPacket = func(network, address string) (net.PacketConn, error) {
			if address == s.UDPRTPAddress {
				return rtpConn, nil
			}
			if address == s.UDPRTCPAddress {
				return rtcpConn, nil
			}
			return nil, fmt.Errorf("server: unexpected UDP bind %q", address)
		}
	}

	if err := s.Start(); err != nil {
		_ = tcpLn.Close()
		return nil, fmt.Errorf("server: start: %w", err)
	}
	return r, nil
}

// RTSPURL returns the loopback rtsp:// URL a publisher should announce to.
func (r *Runner) RTSPURL() string {
	return fmt.Sprintf("rtsp://%s/demo", r.rtspAddr)
}

// Handler exposes the callback handler so callers can wait for teardown.
func (r *Runner) Handler() *Handler { return r.handler }

// Close stops the server and releases every listener, including the UDP
// sockets. It is safe to call after an error or a cancellation.
func (r *Runner) Close() {
	r.server.Close()
}

// allocateUDPPair reserves two consecutive ephemeral UDP ports on loopback,
// an even one for RTP and the next odd one for RTCP, as required by RTSP.
func allocateUDPPair() (rtpConn, rtcpConn *net.UDPConn, rtpPort, rtcpPort int, err error) {
	for attempt := 0; attempt < 16; attempt++ {
		var c1 *net.UDPConn
		c1, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(loopback), Port: 0})
		if err != nil {
			return nil, nil, 0, 0, fmt.Errorf("server: rtp listen: %w", err)
		}
		port := c1.LocalAddr().(*net.UDPAddr).Port
		if port%2 != 0 {
			_ = c1.Close()
			continue // retry until the kernel hands us an even port
		}

		c2, err2 := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(loopback), Port: port + 1})
		if err2 != nil {
			_ = c1.Close()
			continue // the odd neighbour is busy; try another even port
		}
		return c1, c2, port, port + 1, nil
	}
	return nil, nil, 0, 0, errors.New("server: could not allocate consecutive UDP ports")
}
