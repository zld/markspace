package sim

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
)

// Transport selects how the publisher carries RTP to the local server.
type Transport int

const (
	// TransportUDP uses separate UDP datagram sockets.
	TransportUDP Transport = iota
	// TransportTCP interleaves RTP on the RTSP TCP control connection.
	TransportTCP
)

// Description builds the H.264 session description announced by the
// publisher: one video media, a dynamic payload type and a track control
// URL relative to the stream path.
func Description() *description.Session {
	return &description.Session{
		Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Control: "trackID=0",
			Formats: []format.Format{&format.H264{
				PayloadTyp:        PayloadType,
				PacketizationMode: 1,
			}},
		}},
	}
}

// Publisher is a loopback-only RTSP RECORD client driven by a fixture.
type Publisher struct {
	baseURL   string
	transport Transport
	timeout   time.Duration
}

// NewPublisher validates the target address and creates a publisher.
// Only rtsp://127.0.0.1[:port]/... URLs are accepted; no external host can
// ever be reached.
func NewPublisher(rawURL string, transport Transport, timeout time.Duration) (*Publisher, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("sim: invalid URL %q: %w", rawURL, err)
	}
	if u.Scheme != "rtsp" {
		return nil, fmt.Errorf("sim: only rtsp:// URLs are accepted, got %q", u.Scheme)
	}
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
		return nil, fmt.Errorf("sim: refusing non-loopback host %q", u.Hostname())
	}
	return &Publisher{baseURL: rawURL, transport: transport, timeout: timeout}, nil
}

// Publish runs ANNOUNCE, SETUP and RECORD, sends every fixture packet, then
// tears the session down. The context bounds the whole exchange.
func (p *Publisher) Publish(ctx context.Context, fx *Fixture) error {
	mode := gortsplib.TransportUDP
	if p.transport == TransportTCP {
		mode = gortsplib.TransportTCP
	}

	client := &gortsplib.Client{
		Transport:     &mode,
		ReadTimeout:   p.timeout,
		WriteTimeout:  p.timeout,
		AnyPortEnable: true, // server UDP ports are ephemeral, still loopback
	}
	if err := client.StartRecording(p.baseURL, Description()); err != nil {
		return fmt.Errorf("sim: StartRecording failed: %w", err)
	}
	defer client.Close()

	media := Description().Medias[0]
	for _, pkt := range fx.Packets {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := client.WritePacketRTP(media, pkt); err != nil {
			return fmt.Errorf("sim: WritePacketRTP seq=%d failed: %w", pkt.SequenceNumber, err)
		}
	}

	// give the server a moment to drain the last datagrams before teardown
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Millisecond):
	}
	return nil
}
