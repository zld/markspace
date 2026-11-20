package receiver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"jitterlab/internal/jitter"
)

// Sentinel errors let the command layer map UDP failures onto exit codes.
var (
	ErrListen   = errors.New("udp listen failed")
	ErrRead     = errors.New("udp read failed")
	ErrTimeout  = errors.New("udp read timeout")
	ErrCanceled = errors.New("udp receive canceled")
)

// UDPConfig controls a loopback receive session.
type UDPConfig struct {
	Addr     string
	Duration time.Duration
	ReadBuf  int
	// OnReady is called with the bound address once the socket is listening.
	// Tests use it to discover an ephemeral port.
	OnReady func(net.Addr)
}

// UDPResult reports what a listen session observed.
type UDPResult struct {
	Datagrams int
	ReadErrs  int
	Elapsed   time.Duration
	Shutdown  string
}

// Listen binds a UDP socket and feeds received datagrams into ctrl until the
// duration expires, ctx is cancelled, or the socket reports a fatal error.
// Bad packets are counted by the controller and never abort the loop.
func Listen(ctx context.Context, cfg UDPConfig, ctrl *jitter.Controller) (UDPResult, error) {
	if cfg.ReadBuf <= 0 {
		cfg.ReadBuf = 65535
	}
	res := UDPResult{Shutdown: "duration"}
	addr, err := net.ResolveUDPAddr("udp", cfg.Addr)
	if err != nil {
		return res, fmt.Errorf("%w: %v", ErrListen, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return res, fmt.Errorf("%w: %v", ErrListen, err)
	}
	defer conn.Close()
	if cfg.OnReady != nil {
		cfg.OnReady(conn.LocalAddr())
	}

	deadline := time.Now().Add(cfg.Duration)
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()
	defer close(stop)

	finish := func(reason string, elapsed time.Duration) (UDPResult, error) {
		res.Shutdown = reason
		res.Elapsed = elapsed
		// Live playout runs during the session; because the controller clock
		// follows arrival time, the samples and the buffered tail must be
		// advanced to the shutdown instant before the caller drains.
		ctrl.AdvanceTo(elapsed)
		return res, nil
	}
	// A short read deadline keeps the loop responsive to cancellation and to
	// the session deadline; it is not an error while the session is live.
	const keepAlive = 50 * time.Millisecond
	if cfg.Duration > 0 {
		if err := conn.SetReadDeadline(time.Now().Add(minDuration(cfg.Duration, keepAlive))); err != nil {
			return res, fmt.Errorf("%w: %v", ErrRead, err)
		}
	}

	start := time.Now()
	buf := make([]byte, cfg.ReadBuf)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		now := time.Since(start)
		if err != nil {
			if ctx.Err() != nil {
				return finish("signal", now)
			}
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				// Playout is driven by wall time, not by arrivals: advance the
				// controller even when the socket is silent, so the buffer
				// drains and the watermark keeps being sampled.
				ctrl.AdvanceTo(now)
				if cfg.Duration > 0 && !time.Now().Before(deadline) {
					return finish("timeout", now)
				}
				// Keep-alive deadline fired while the session is still live.
				next := keepAlive
				if cfg.Duration > 0 {
					next = minDuration(time.Until(deadline), keepAlive)
				}
				if err := conn.SetReadDeadline(time.Now().Add(next)); err != nil {
					res.ReadErrs++
					return res, fmt.Errorf("%w: %v", ErrRead, err)
				}
				continue
			}
			// A closed socket after cancellation is expected; anything else is
			// a genuine receive failure.
			if errors.Is(err, net.ErrClosed) {
				return finish("signal", now)
			}
			res.ReadErrs++
			return res, fmt.Errorf("%w: %v", ErrRead, err)
		}
		res.Datagrams++
		ctrl.AddPacket(buf[:n], now)
		ctrl.AdvanceTo(now)
		if cfg.Duration <= 0 {
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return finish("duration", time.Since(start))
		}
		if err := conn.SetReadDeadline(time.Now().Add(minDuration(remaining, keepAlive))); err != nil {
			res.ReadErrs++
			return res, fmt.Errorf("%w: %v", ErrRead, err)
		}
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
