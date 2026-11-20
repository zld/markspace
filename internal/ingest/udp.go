// Package ingest receives and validates packets from UDP datagrams and
// length-prefixed TCP streams on loopback.
package ingest

import (
	"context"
	"errors"
	"net"
	"time"

	"jitterlab/internal/wire"
)

// Handler receives validated packets and malformed-datagram events.
type Handler struct {
	OnPacket   func(wire.Packet)
	OnMalformed func()
}

// ioTimeout bounds a single network read so loops can notice
// cancellation and idle peers.
const ioTimeout = 500 * time.Millisecond

// ServeUDP reads datagrams from conn until ctx is cancelled or a fatal
// read error occurs. conn is closed on return.
func ServeUDP(ctx context.Context, conn *net.UDPConn, h Handler) error {
	defer conn.Close()
	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		_ = conn.SetReadDeadline(time.Now().Add(ioTimeout))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if n > wire.MaxPacket {
			h.OnMalformed()
			continue
		}
		pkt, err := wire.Decode(buf[:n])
		if err != nil {
			h.OnMalformed()
			continue
		}
		h.OnPacket(pkt)
	}
}
