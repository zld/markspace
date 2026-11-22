package transport

import "time"

// UDPConfig is the configuration of a UDP RTP transport session.
type UDPConfig struct {
	// ServerURL must point at a loopback rtsp:// server.
	ServerURL string
	// ExpectedPackets is how many RTP packets the server will send.
	ExpectedPackets int
	// RTPTimeout bounds packet draining; zero uses the runner default.
	RTPTimeout time.Duration
}

// RunUDP runs the full RTSP session with RTP/RTCP carried over UDP.
//
// The gortsplib client opens separate UDP listeners for RTP and RTCP;
// session state transitions and frame reassembly are handled by the
// shared Runner logic, identical to the interleaved mode.
func RunUDP(cfg UDPConfig) (*Result, error) {
	r, err := NewRunner(Config{
		Mode:            ModeUDP,
		ServerURL:       cfg.ServerURL,
		ExpectedPackets: cfg.ExpectedPackets,
		RTPTimeout:      cfg.RTPTimeout,
	})
	if err != nil {
		return nil, err
	}
	return r.Run(), nil
}
