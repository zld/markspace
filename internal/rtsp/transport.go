package rtsp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/pion/rtp"

	"rtspdiag/internal/config"
)

// Transport constants re-exported so callers depend on our package only.
var (
	gortTransportUDP = gortsplib.TransportUDP
	gortTransportTCP = gortsplib.TransportTCP
)

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
	Transport       config.TransportMode
	URL             string
	Window          int
	ReorderTimeout  time.Duration
	// ReadTimeout bounds blocking RTSP reads so a dead server cannot hang.
	ReadTimeout time.Duration
	Pipeline    Pipeline
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
// or a fatal protocol error occurs.
func (a *Adapter) Run(ctx context.Context) error {
	transport := gortTransportUDP
	if a.cfg.Transport == config.TransportTCP {
		transport = gortTransportTCP
	}
	client := &gortsplib.Client{
		Transport:            &transport,
		ReadTimeout:          a.cfg.ReadTimeout,
		WriteTimeout:         a.cfg.ReadTimeout,
		InitialUDPReadTimeout: 2 * time.Second,
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

	// Every RTP packet, regardless of transport, enters through one callback.
	client.OnPacketRTP(media, media.Formats[0], a.onPacket)

	if _, err := client.Play(nil); err != nil {
		return fmt.Errorf("rtsp: play: %w", err)
	}
	if err := a.session.Apply(MethodPlay); err != nil {
		return err
	}

	// Block until the context is cancelled or the server disconnects. Wait
	// surfaces connection errors including forced server-side closes.
	err = a.waitUntilDone(ctx, client)

	if teardownErr := a.session.Apply(MethodTeardown); teardownErr != nil {
		// A disconnect can make TEARDOWN illegal on the wire; the state machine
		// still records nothing and we keep the original disconnect cause.
		a.mu.Lock()
		disconnected := !a.gotData || err != nil
		a.mu.Unlock()
		if !disconnected {
			return teardownErr
		}
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
