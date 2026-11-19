// Package transport adapts the gortsplib client to the two supported
// delivery modes: interleaved TCP and unicast UDP over loopback.
// Both adapters share one state machine and one RTP processing
// pipeline; only the client transport configuration differs.
package transport

import (
	"errors"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	pionrtp "github.com/pion/rtp"
)

// Mode selects the RTP delivery mechanism.
type Mode string

const (
	// ModeTCP uses RTP/RTCP interleaved on the RTSP control connection.
	ModeTCP Mode = "tcp"
	// ModeUDP uses separate loopback UDP sockets.
	ModeUDP Mode = "udp"
)

// ParseMode validates a command-line mode string.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeTCP:
		return ModeTCP, nil
	case ModeUDP:
		return ModeUDP, nil
	default:
		return "", errors.New("transport: mode must be \"tcp\" or \"udp\"")
	}
}

// Config holds the adapter-independent client settings.
type Config struct {
	Mode Mode
	// Host is the RTSP server host:port (always loopback here).
	Host string
	// UDPMinPort/UDPMaxPort bound the client UDP socket range.
	// Ignored in TCP mode.
	UDPMinPort int
	UDPMaxPort int
	// ReadTimeout/WriteTimeout cap RTSP control-channel operations.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// ErrPortExhausted means no usable port exists in the configured range.
var ErrPortExhausted = errors.New("transport: no free UDP port in configured range")

// ErrInvalidMode means the requested transport mode is unsupported.
var ErrInvalidMode = errors.New("transport: unsupported transport mode")

// RawHandler receives the re-serialized RTP datagram exactly as it
// arrived on the wire, so the self-written parser stays in the live
// data path for both transport modes.
type RawHandler func(raw []byte)

// Adapter is implemented by both delivery modes.
type Adapter interface {
	Mode() Mode
	// Client exposes the configured, not yet started gortsplib client.
	Client() *gortsplib.Client
	// OnRTP registers the raw RTP datagram handler.
	OnRTP(RawHandler)
	// MarkStarted records that the underlying client was started,
	// so Close can distinguish a live client from a failed pre-start.
	MarkStarted()
	// Close releases the control connection and, for UDP, every
	// socket the adapter opened. It is safe to call repeatedly.
	Close() error
}

// wireCallback re-serializes each received pion packet and hands the
// raw bytes to the project's own parser, keeping parsing identical
// across the two transport modes.
func wireCallback(onRTP RawHandler) func(*description.Media, format.Format, *pionrtp.Packet) {
	return func(_ *description.Media, _ format.Format, pkt *pionrtp.Packet) {
		if onRTP == nil {
			return
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return
		}
		onRTP(raw)
	}
}

// newBaseClient builds the gortsplib settings shared by both modes.
func newBaseClient(cfg Config) *gortsplib.Client {
	return &gortsplib.Client{
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		// Refuse automatic UDP->TCP fallback so mode differences
		// stay observable in the report.
		AnyPortEnable: false,
	}
}
