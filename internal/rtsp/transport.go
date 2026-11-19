package rtsp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"

	"rtspdiag/internal/config"
)

// gortTransportTCP is the gortsplib RTP-over-TCP transport selector.
var gortTransportTCP = gortsplib.TransportTCP

// Pipeline receives every parsed RTP packet in delivery order. It is
// implemented by the reorder/frame wiring in the rtspdiag runner.
type Pipeline interface {
	// Ingest accepts one raw RTP datagram and returns an error if parsing
	// fails; the adapter counts parse failures instead of aborting.
	Ingest(raw []byte, now time.Time) error
	// ParseErrors reports how many datagrams failed RTP parsing.
	ParseErrors() int
}

// AdapterConfig parameterizes one client-side transport adapter.
type AdapterConfig struct {
	Transport      config.TransportMode
	URL            string
	Window         int
	ReorderTimeout time.Duration
	// ReadTimeout bounds blocking RTSP reads so a dead server cannot hang.
	ReadTimeout time.Duration
	Pipeline    Pipeline
	// InjectMalformed, when non-nil, is fed straight into the pipeline after
	// PLAY so the project-owned parser records a parse error. It models a
	// corrupted datagram that never reaches gortsplib's own decoder.
	InjectMalformed []byte
	// AnyPortEnable relaxes gortsplib's strict UDP source-port filtering.
	AnyPortEnable bool
	// WrapPacketConn, when set, decorates each client UDP PacketConn. It is
	// used for diagnostics and socket-level observation.
	WrapPacketConn func(net.PacketConn) net.PacketConn
}

// Adapter drives the shared RTSP state machine over either UDP or
// RTP-over-TCP. Both transports call the same Session.Apply for every method.
type Adapter struct {
	cfg     AdapterConfig
	session *Session

	mu      sync.Mutex
	closed  bool
	gotData bool
}

// NewAdapter builds an adapter with a fresh state machine.
func NewAdapter(cfg AdapterConfig) *Adapter {
	return &Adapter{cfg: cfg, session: NewSession()}
}

// Session exposes the state machine for transition inspection.
func (a *Adapter) Session() *Session { return a.session }

// Run performs the full OPTIONS..PLAY..TEARDOWN exchange against the in-process
// mock server and streams RTP until the context ends, the server disconnects,
// or a fatal protocol error occurs. UDP uses a self-managed data plane so the
// project-owned reorderer handles gaps; TCP uses gortsplib interleaved frames.
// Both paths drive the same Session state machine and Pipeline.
func (a *Adapter) Run(ctx context.Context) error {
	if a.cfg.Transport == config.TransportUDP {
		return a.runUDP(ctx)
	}
	return a.runTCP(ctx)
}

// runTCP uses gortsplib's RTP-over-TCP (interleaved) transport. TCP provides
// reliable, ordered delivery, so gortsplib's reorder buffer never stalls on a
// network gap and the callback path is sufficient.
func (a *Adapter) runTCP(ctx context.Context) error {
	transport := gortTransportTCP
	client := &gortsplib.Client{
		Transport:             &transport,
		ReadTimeout:           a.cfg.ReadTimeout,
		WriteTimeout:          a.cfg.ReadTimeout,
		InitialUDPReadTimeout: 2 * time.Second,
		AnyPortEnable:         a.cfg.AnyPortEnable,
		// Swallow gortsplib's default log lines; loss and decode events are
		// measured by the project pipeline instead of printing to stderr.
		OnPacketLost:  func(error) {},
		OnDecodeError: func(error) {},
	}
	if a.cfg.WrapPacketConn != nil {
		client.ListenPacket = func(network, address string) (net.PacketConn, error) {
			pc, err := net.ListenPacket(network, address)
			if err != nil {
				return nil, err
			}
			return a.cfg.WrapPacketConn(pc), nil
		}
	}
	if err := client.Start(a.scheme(), a.host()); err != nil {
		return fmt.Errorf("rtsp: client start: %w", err)
	}
	defer a.cleanup(client)

	u, err := base.ParseURL(a.cfg.URL)
	if err != nil {
		return fmt.Errorf("rtsp: parse url: %w", err)
	}

	if _, err := client.Options(u); err != nil {
		return fmt.Errorf("rtsp: options: %w", err)
	}
	if err := a.session.Apply(MethodOptions); err != nil {
		return err
	}

	desc, _, err := client.Describe(u)
	if err != nil {
		return fmt.Errorf("rtsp: describe: %w", err)
	}
	if err := a.session.Apply(MethodDescribe); err != nil {
		return err
	}
	if len(desc.Medias) == 0 {
		return errors.New("rtsp: server offered no media")
	}
	media := desc.Medias[0]

	if _, err := client.Setup(desc.BaseURL, media, 0, 0); err != nil {
		return fmt.Errorf("rtsp: setup: %w", err)
	}
	if err := a.session.Apply(MethodSetup); err != nil {
		return err
	}

	// RTP-over-TCP packets enter through one callback.
	client.OnPacketRTPAny(func(_ *description.Media, _ format.Format, pkt *rtp.Packet) {
		a.onPacket(pkt)
	})

	if _, err := client.Play(nil); err != nil {
		return fmt.Errorf("rtsp: play: %w", err)
	}
	if err := a.session.Apply(MethodPlay); err != nil {
		return err
	}

	if len(a.cfg.InjectMalformed) > 0 {
		_ = a.cfg.Pipeline.Ingest(a.cfg.InjectMalformed, time.Now())
	}

	err = a.waitUntilDone(ctx, client)
	if teardownErr := a.session.Apply(MethodTeardown); teardownErr != nil && err == nil {
		return teardownErr
	}
	return err
}

func (a *Adapter) onPacket(pkt *rtp.Packet) {
	raw, err := pkt.Marshal()
	if err != nil {
		return
	}
	a.mu.Lock()
	a.gotData = true
	a.mu.Unlock()
	// Parse errors are counted inside the pipeline; never panic on a packet.
	_ = a.cfg.Pipeline.Ingest(raw, time.Now())
}

// waitUntilDone blocks on context cancellation or the client's Wait error.
func (a *Adapter) waitUntilDone(ctx context.Context, client *gortsplib.Client) error {
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("rtsp: session ended: %w", err)
		}
		return nil
	}
}

// cleanup tears down the client and marks the adapter closed so no buffered
// session state survives into a later run.
func (a *Adapter) cleanup(client *gortsplib.Client) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	a.mu.Unlock()
	client.Close()
}

func (a *Adapter) scheme() string {
	if u, err := base.ParseURL(a.cfg.URL); err == nil && u.Scheme != "" {
		return u.Scheme
	}
	return "rtsp"
}

func (a *Adapter) host() string {
	if u, err := base.ParseURL(a.cfg.URL); err == nil {
		return u.Host
	}
	return ""
}

// Reset clears the state machine so the next run starts from INIT.
func (a *Adapter) Reset() {
	a.session.Reset()
	a.mu.Lock()
	a.gotData = false
	a.closed = false
	a.mu.Unlock()
}
