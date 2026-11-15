// Package udp receives datagrams on a loopback address and feeds the
// shared receive pipeline in real time.
package udp

import (
	"context"
	"errors"
	"net"
	"time"

	"streamlab/internal/receive"
)

// MaxDatagram bounds accepted datagram size; larger reads are truncated
// and rejected later by wire validation.
const MaxDatagram = 8192

// Run receives on conn until dur elapses or ctx is cancelled, then
// returns. Read timeouts drive the playout clock so idle periods still
// produce concealment events. The pipeline is the same implementation
// used by the simulator.
func Run(ctx context.Context, conn net.PacketConn, pipe *receive.Pipeline, dur time.Duration) error {
	start := time.Now()
	deadline := start.Add(dur)
	buf := make([]byte, MaxDatagram)
	for {
		now := time.Now()
		if !now.Before(deadline) {
			return nil
		}
		_ = conn.SetReadDeadline(now.Add(5 * time.Millisecond))
		n, _, err := conn.ReadFrom(buf)
		ms := time.Since(start).Milliseconds()
		switch {
		case err == nil:
			pipe.OnDatagram(buf[:n], ms)
		case isTimeout(err):
			// no data: fall through to playout advance
		default:
			return err
		}
		pipe.AdvanceTo(ms)
		if err := ctx.Err(); err != nil {
			return nil // signal-driven shutdown is a clean exit
		}
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
