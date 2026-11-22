package transport

import "time"

// InterleavedConfig is the configuration of a TCP-interleaved session.
type InterleavedConfig struct {
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

// RunInterleaved runs the full RTSP session with RTP/RTCP multiplexed
// on the RTSP TCP control connection (RFC 2326, section 10.12).
//
// No separate UDP sockets are opened. The same shared Runner drives
// the state machine and the same frame assembler as RunUDP, so both
// transports produce directly comparable statistics.
func RunInterleaved(cfg InterleavedConfig) (*Result, error) {
	r, err := NewRunner(Config{
		Mode:               ModeInterleaved,
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
