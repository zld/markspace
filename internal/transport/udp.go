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
	// DrainQuietPeriod ends draining after a packet-arrival pause;
	// zero uses the runner default.
	DrainQuietPeriod time.Duration
	// ExpectedLastSeq / HasExpectedLastSeq / SSRC describe the
	// scenario's true stream tail for deterministic gap accounting.
	ExpectedLastSeq    uint16
	HasExpectedLastSeq bool
	SSRC               uint32
}

// RunUDP runs the full RTSP session with RTP/RTCP carried over UDP.
//
// The gortsplib client opens separate UDP listeners for RTP and RTCP;
// session state transitions and frame reassembly are handled by the
// shared Runner logic, identical to the interleaved mode.
func RunUDP(cfg UDPConfig) (*Result, error) {
	r, err := NewRunner(Config{
		Mode:               ModeUDP,
		ServerURL:          cfg.ServerURL,
		ExpectedPackets:    cfg.ExpectedPackets,
		RTPTimeout:         cfg.RTPTimeout,
		DrainQuietPeriod:   cfg.DrainQuietPeriod,
		ExpectedLastSeq:    cfg.ExpectedLastSeq,
		HasExpectedLastSeq: cfg.HasExpectedLastSeq,
		SSRC:               cfg.SSRC,
	})
	if err != nil {
		return nil, err
	}
	return r.Run(), nil
}
