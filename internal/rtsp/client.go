// Package rtsp wraps a gortsplib client, driving the shared lifecycle
// machine and the RTP reorder/H.264 reassembly pipeline.
package rtsp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	pionrtp "github.com/pion/rtp"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"

	"rtsp-audit/internal/lifecycle"
	"rtsp-audit/internal/rtp"
)

// TransportMode selects the RTP transport.
type TransportMode string

const (
	// TransportUDP is plain RTP over UDP.
	TransportUDP TransportMode = "udp"
	// TransportTCP is RTP interleaved in the RTSP TCP connection.
	TransportTCP TransportMode = "tcp-interleaved"
)

// ErrExternalURL is returned when the target is not a loopback address.
var ErrExternalURL = fmt.Errorf("only loopback RTSP addresses are allowed")

// Stats collects everything measured during one session.
type Stats struct {
	Transitions         int              `json:"transition_count"`
	RejectedTransitions int              `json:"rejected_transitions"`
	RTP                 rtp.ReorderStats `json:"rtp"`
	H264                rtp.H264Stats    `json:"h264"`
}

// Run executes one full RTSP session against rawURL and returns the
// collected statistics. Partial statistics are returned together with
// an error on protocol failures, timeouts or early disconnects.
func Run(ctx context.Context, rawURL string, mode TransportMode, idleTimeout time.Duration) (*Stats, error) {
	u, err := base.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if err := checkLoopback(u); err != nil {
		return nil, err
	}

	machine := lifecycle.New()
	re := rtp.NewReorderer()
	fa := rtp.NewFrameReassembler()

	var mu sync.Mutex
	var lastPacket time.Time

	transport := gortsplib.TransportUDP
	if mode == TransportTCP {
		transport = gortsplib.TransportTCP
	}
	c := &gortsplib.Client{
		Transport:    &transport,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	stats := &Stats{}
	collect := func() {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range re.Flush() {
			fa.Process(p)
		}
		fa.Flush()
		stats.Transitions = machine.TransitionCount()
		stats.RejectedTransitions = machine.RejectedCount()
		stats.RTP = re.Stats()
		stats.H264 = fa.Stats()
	}

	var runErr error
	step := func(ev lifecycle.Event, fn func() error) bool {
		if err := fn(); err != nil {
			runErr = err
			return false
		}
		// a rejected transition here would indicate a logic bug; it
		// is counted and leaves the state untouched
		_ = machine.Transition(ev)
		return true
	}

	if err := c.Start(u.Scheme, u.Host); err != nil {
		collect()
		return stats, fmt.Errorf("connect: %w", err)
	}
	defer c.Close()

	if !step(lifecycle.EventOptions, func() error { _, err := c.Options(u); return err }) {
		collect()
		return stats, fmt.Errorf("options: %w", runErr)
	}
	var desc *description.Session
	if !step(lifecycle.EventDescribe, func() error {
		var err error
		desc, _, err = c.Describe(u)
		return err
	}) {
		collect()
		return stats, fmt.Errorf("describe: %w", runErr)
	}
	if !step(lifecycle.EventSetup, func() error { return c.SetupAll(desc.BaseURL, desc.Medias) }) {
		collect()
		return stats, fmt.Errorf("setup: %w", runErr)
	}
	c.OnPacketRTPAny(func(medi *description.Media, forma format.Format, pkt *pionrtp.Packet) {
		mu.Lock()
		defer mu.Unlock()
		lastPacket = time.Now()
		for _, p := range re.Feed(pkt) {
			fa.Process(p)
		}
	})
	if !step(lifecycle.EventPlay, func() error { _, err := c.Play(nil); return err }) {
		collect()
		return stats, fmt.Errorf("play: %w", runErr)
	}

	// wait until the stream goes idle (or the context deadline hits);
	// the zero-packet guard also terminates empty streams
	playedAt := time.Now()
	for runErr == nil {
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
		case <-time.After(20 * time.Millisecond):
			mu.Lock()
			switch {
			case !lastPacket.IsZero() && time.Since(lastPacket) > idleTimeout:
				runErr = errStreamIdle
			case lastPacket.IsZero() && time.Since(playedAt) > 2*idleTimeout:
				runErr = errStreamIdle
			}
			mu.Unlock()
		}
	}

	_ = machine.Transition(lifecycle.EventClose)
	_ = machine.Transition(lifecycle.EventEnd)
	collect()
	if runErr == errStreamIdle {
		return stats, nil
	}
	return stats, fmt.Errorf("session: %w", runErr)
}

var errStreamIdle = fmt.Errorf("stream idle")

// checkLoopback rejects any non-loopback RTSP target.
func checkLoopback(u *base.URL) error {
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%w: %q", ErrExternalURL, host)
	}
	return nil
}
