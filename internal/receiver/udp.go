package receiver

import (
	"errors"
	"fmt"
	"net"
	"time"

	"jitterlab/internal/jitter"
	"jitterlab/internal/protocol"
)

// pollSlice bounds how long a single read waits so playout deadlines are
// serviced even when no datagrams arrive.
const pollSlice = 10 * time.Millisecond

// Listen binds a UDP socket (callers should pass a loopback address),
// receives datagrams for duration, and drives ctl with real-time arrivals.
// Malformed datagrams are counted and skipped; they never fail the run.
// On timeout the controller is drained and nil is returned.
func Listen(addr string, duration time.Duration, ctl *jitter.Controller, maxPayload int) error {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", ua)
	if err != nil {
		return fmt.Errorf("bind %q: %w", addr, err)
	}
	defer conn.Close()

	start := time.Now()
	deadline := start.Add(duration)
	buf := make([]byte, 64*1024)
	for {
		now := time.Now()
		if !now.Before(deadline) {
			break
		}
		slice := now.Add(pollSlice)
		if slice.After(deadline) {
			slice = deadline
		}
		if err := conn.SetReadDeadline(slice); err != nil {
			return fmt.Errorf("set deadline: %w", err)
		}
		n, _, err := conn.ReadFromUDP(buf)
		at := time.Since(start)
		ctl.PlayoutUntil(at)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue // idle slice: playout already advanced
			}
			return fmt.Errorf("read: %w", err)
		}
		pkt, err := protocol.Decode(buf[:n], maxPayload)
		if err != nil {
			ctl.M.Malformed++
			continue
		}
		ctl.AddPacket(pkt, at)
	}
	ctl.PlayoutUntil(time.Since(start))
	ctl.Drain()
	return nil
}
