package rtsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"

	"rtspcheck/internal/session"
)

// Transport names accepted by the client driver.
const (
	TransportUDP = "udp"
	TransportTCP = "tcp"
)

// ClientDeps groups the collaborators a client run needs.
type ClientDeps struct {
	Transport string
	Timeout   time.Duration
	Machine   *session.Machine
	// OnPacket receives every RTP packet in gortsplib delivery order.
	OnPacket func(*rtp.Packet)
}

// RunClient executes the full RTSP client lifecycle against a mock server
// URL. Each successful stage emits one state-machine event. The function
// returns once the client enters PLAY; callers use WaitAndClose to finish.
// Every exit path closes the gortsplib client so transport resources and
// the control connection cannot leak.
func RunClient(ctx context.Context, rawURL string, deps ClientDeps) (*gortsplib.Client, error) {
	u, err := base.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("rtsp client: parse url: %w", err)
	}
	if u.Hostname() != loopbackHost {
		return nil, fmt.Errorf("rtsp client: refusing non-loopback target %q", rawURL)
	}

	transport, err := parseTransport(deps.Transport)
	if err != nil {
		return nil, err
	}

	client := &gortsplib.Client{
		Transport:             &transport,
		ReadTimeout:           deps.Timeout,
		WriteTimeout:          deps.Timeout,
		InitialUDPReadTimeout: 2 * time.Second,
	}

	if err := client.Start(u.Scheme, u.Host); err != nil {
		return nil, stageError("connect", err)
	}
	if err := deps.Machine.Handle(session.EvConnectOK); err != nil {
		client.Close()
		return nil, err
	}

	desc, _, err := client.Describe(u)
	if err != nil {
		client.Close()
		return nil, stageError("describe", err)
	}
	if err := deps.Machine.Handle(session.EvDescribeOK); err != nil {
		client.Close()
		return nil, err
	}

	var forma *format.H264
	medi := desc.FindFormat(&forma)
	if medi == nil || forma == nil {
		client.Close()
		return nil, stageError("describe", fmt.Errorf("no H.264 media in SDP"))
	}

	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		client.Close()
		return nil, stageError("setup", err)
	}
	if err := deps.Machine.Handle(session.EvSetupOK); err != nil {
		client.Close()
		return nil, err
	}

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		if deps.OnPacket != nil {
			deps.OnPacket(pkt)
		}
	})

	if _, err := client.Play(nil); err != nil {
		client.Close()
		return nil, stageError("play", err)
	}
	if err := deps.Machine.Handle(session.EvPlayOK); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// WaitAndClose blocks until ctx is canceled, the server ends the session or
// a fatal client error occurs, then closes the client. It never blocks for
// longer than ctx plus a short cleanup grace period.
func WaitAndClose(ctx context.Context, client *gortsplib.Client) error {
	waitErr := make(chan error, 1)
	go func() { waitErr <- client.Wait() }()

	var err error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-waitErr:
	}

	// Close stops the control connection and UDP/TCP transport routines.
	done := make(chan struct{})
	go func() {
		client.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(gracePeriod):
	}

	// io.EOF is the orderly stream-end signal produced when the mock closes
	// the PLAY session after sending the full manifest; it is not a failure.
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// gracePeriod bounds a Close call so a stuck peer cannot hang cleanup.
const gracePeriod = time.Second

// StageError tags an error with the RTSP phase that failed so callers can
// locate failures precisely.
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("rtsp stage %s failed: %v", e.Stage, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }

func stageError(stage string, err error) error {
	return &StageError{Stage: stage, Err: err}
}

func parseTransport(name string) (gortsplib.Transport, error) {
	switch name {
	case TransportUDP:
		return gortsplib.TransportUDP, nil
	case TransportTCP:
		return gortsplib.TransportTCP, nil
	default:
		return 0, fmt.Errorf("rtsp client: unknown transport %q", name)
	}
}
