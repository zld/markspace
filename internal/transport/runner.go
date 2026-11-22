// Package transport adapts the gortsplib client to the two required
// RTP transports (UDP and RTSP TCP-interleaved). Both adapters share
// the same Runner, the same session.Machine and the same rtp.Assembler,
// so any difference in observed behavior reflects the transport alone.
package transport

import (
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/pion/rtp"

	rtpx "rtspcheck/internal/rtp"
	"rtspcheck/internal/session"
)

// Mode names a supported RTP transport.
type Mode string

// Supported transport modes.
const (
	ModeUDP         Mode = "udp"
	ModeInterleaved Mode = "interleaved"
)

// Config drives one transport session.
type Config struct {
	// Mode selects UDP or TCP-interleaved.
	Mode Mode
	// ServerURL is an rtsp:// URL pointing at the local simulated
	// server; only loopback hosts are accepted.
	ServerURL string
	// RTPTimeout bounds how long the runner waits for the packet
	// stream to drain after PLAY returns.
	RTPTimeout time.Duration
	// ExpectedPackets is the number of RTP packets the server sends
	// on a healthy run; draining stops as soon as all are seen.
	ExpectedPackets int
}

// Result is everything observed during one session, including the
// partial data available after a failure.
type Result struct {
	Transport     Mode
	Machine       *session.Machine
	Assembler     *rtpx.Assembler
	PacketsSeen   int
	Failure       error
	FailureStage  session.State
	FailureMethod session.Method
}

// Runner executes the OPTIONS/DESCRIBE/SETUP/PLAY/TEARDOWN sequence
// against a local RTSP server using one explicitly selected transport.
type Runner struct {
	cfg  Config
	sm   *session.Machine
	asm  *rtpx.Assembler

	mu   sync.Mutex
	seen int
}

// NewRunner validates the configuration and creates a runner.
func NewRunner(cfg Config) (*Runner, error) {
	if cfg.Mode != ModeUDP && cfg.Mode != ModeInterleaved {
		return nil, fmt.Errorf("invalid transport mode %q", cfg.Mode)
	}
	u, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}
	if u.Scheme != "rtsp" {
		return nil, fmt.Errorf("only rtsp:// URLs are supported, got %q", u.Scheme)
	}
	switch host := u.Hostname(); host {
	case "127.0.0.1", "localhost", "::1":
	default:
		return nil, fmt.Errorf("refusing non-loopback target %q", host)
	}
	if cfg.RTPTimeout <= 0 {
		cfg.RTPTimeout = 5 * time.Second
	}
	return &Runner{
		cfg: cfg,
		sm:  session.NewMachine(),
		asm: rtpx.NewAssembler(),
	}, nil
}

// Run performs the full session and always returns a Result, even on
// failure. On failure the result carries the error and every state
// transition and packet observed up to that point.
func (r *Runner) Run() *Result {
	res := &Result{Transport: r.cfg.Mode, Machine: r.sm, Assembler: r.asm}

	tr := gortsplib.TransportUDP
	if r.cfg.Mode == ModeInterleaved {
		tr = gortsplib.TransportTCP
	}
	client := &gortsplib.Client{
		Transport:             &tr,
		ReadTimeout:           5 * time.Second,
		WriteTimeout:          5 * time.Second,
		InitialUDPReadTimeout: 3 * time.Second,
	}

	u, err := base.ParseURL(r.cfg.ServerURL)
	if err != nil {
		r.fail(res, session.MethodOptions, err)
		return res
	}

	// OPTIONS (client.Start opens the TCP control connection and
	// performs the initial OPTIONS exchange internally).
	if err = client.Start(u.Scheme, u.Host); err != nil {
		r.fail(res, session.MethodOptions, err)
		return res
	}
	if err = r.sm.Apply(session.Event{Method: session.MethodOptions, OK: true}); err != nil {
		r.recordFailure(res, err)
		return res
	}

	// DESCRIBE
	desc, _, err := client.Describe(u)
	if err != nil {
		r.fail(res, session.MethodDescribe, err)
		return res
	}
	if err = r.sm.Apply(session.Event{Method: session.MethodDescribe, OK: true}); err != nil {
		r.recordFailure(res, err)
		return res
	}

	if len(desc.Medias) == 0 {
		r.fail(res, session.MethodSetup, fmt.Errorf("server offered no medias"))
		return res
	}
	media := desc.Medias[0]
	client.OnPacketRTP(media, media.Formats[0], r.handlePacket)

	// SETUP
	if _, err = client.Setup(u, media, 0, 0); err != nil {
		r.fail(res, session.MethodSetup, err)
		return res
	}
	if err = r.sm.Apply(session.Event{Method: session.MethodSetup, OK: true}); err != nil {
		r.recordFailure(res, err)
		return res
	}

	// PLAY
	if _, err = client.Play(nil); err != nil {
		r.fail(res, session.MethodPlay, err)
		return res
	}
	if err = r.sm.Apply(session.Event{Method: session.MethodPlay, OK: true}); err != nil {
		r.recordFailure(res, err)
		return res
	}

	// Drain the deterministic stream. Seeing every expected packet is
	// the success path; the timeout covers interrupted sessions whose
	// server severed the connection mid-stream.
	deadline := time.Now().Add(r.cfg.RTPTimeout)
	for r.packetsSeen() < r.cfg.ExpectedPackets {
		if time.Now().After(deadline) {
			r.fail(res, session.MethodTeardown, fmt.Errorf(
				"stream interrupted: received %d/%d packets",
				r.packetsSeen(), r.cfg.ExpectedPackets))
			r.asm.FlushOpen()
			client.Close()
			return res
		}
		time.Sleep(2 * time.Millisecond)
	}

	// TEARDOWN is issued by client.Close(). A broken control
	// connection surfaces through Wait().
	client.Close()
	if waitErr := client.Wait(); waitErr != nil {
		r.fail(res, session.MethodTeardown, waitErr)
		return res
	}
	if err = r.sm.Apply(session.Event{Method: session.MethodTeardown, OK: true}); err != nil {
		r.recordFailure(res, err)
		return res
	}
	if err = r.sm.Close(); err != nil {
		r.recordFailure(res, err)
		return res
	}
	res.PacketsSeen = r.packetsSeen()
	return res
}

func (r *Runner) handlePacket(pkt *rtp.Packet) {
	r.asm.Push(pkt)
	r.mu.Lock()
	r.seen++
	r.mu.Unlock()
}

func (r *Runner) packetsSeen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen
}

// fail records a transport/response failure at the given method stage
// and moves the machine into Failed.
func (r *Runner) fail(res *Result, method session.Method, err error) {
	_ = r.sm.Apply(session.Event{
		Method:       method,
		OK:           false,
		TransportErr: true,
	})
	res.Failure = err
	res.FailureStage = r.sm.FailedStage()
	res.FailureMethod = r.sm.FailedMethod()
	res.PacketsSeen = r.packetsSeen()
}

// recordFailure is used when the machine itself rejected a transition.
func (r *Runner) recordFailure(res *Result, err error) {
	res.Failure = err
	res.FailureStage = r.sm.FailedStage()
	res.FailureMethod = r.sm.FailedMethod()
	res.PacketsSeen = r.packetsSeen()
}
