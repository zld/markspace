// Package transport wraps UDP datagram reception with explicit handling of
// read deadlines, connection close and truncated datagrams. Only loopback
// addresses are used by jbbench.
package transport

import (
	"errors"
	"net"
	"sync/atomic"
	"time"

	"jbbench/internal/protocol"
)

// Sentinel errors reported by ReadPacket.
var (
	// ErrTimeout means the read deadline elapsed with no datagram.
	ErrTimeout = errors.New("transport: read deadline exceeded")
	// ErrClosed means the listener was closed while blocked on a read.
	ErrClosed = errors.New("transport: listener closed")
	// ErrTruncated means the datagram was larger than one protocol packet and
	// cannot be a valid fixed-format frame.
	ErrTruncated = errors.New("transport: datagram exceeds maximum packet size")
)

// Listener owns one bound UDP socket.
type Listener struct {
	conn   *net.UDPConn
	buf    []byte
	closed atomic.Bool
}

// NewListener binds a UDP socket on addr (e.g. "127.0.0.1:0"). The receive
// buffer is sized for one maximum protocol packet.
func NewListener(addr string) (*Listener, error) {
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", a)
	if err != nil {
		return nil, err
	}
	return &Listener{
		conn: conn,
		buf:  make([]byte, protocol.MaxPacketSize*2),
	}, nil
}

// LocalAddr returns the bound address, useful when binding to port 0.
func (l *Listener) LocalAddr() *net.UDPAddr {
	return l.conn.LocalAddr().(*net.UDPAddr)
}

// ReadPacket waits up to timeout for the next datagram and returns its bytes
// (copied, so the slice survives the next call) and sender address.
func (l *Listener) ReadPacket(timeout time.Duration) ([]byte, *net.UDPAddr, error) {
	if l.closed.Load() {
		return nil, nil, ErrClosed
	}
	if err := l.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, nil, err
	}
	n, src, err := l.conn.ReadFromUDP(l.buf)
	if err != nil {
		if l.closed.Load() || isClosedErr(err) {
			return nil, nil, ErrClosed
		}
		if isTimeoutErr(err) {
			return nil, nil, ErrTimeout
		}
		return nil, nil, err
	}
	out := make([]byte, n)
	copy(out, l.buf[:n])
	if n > protocol.MaxPacketSize {
		return out, src, ErrTruncated
	}
	return out, src, nil
}

// Close releases the socket. It is safe to call concurrently or multiple
// times; only the first call closes the underlying connection.
func (l *Listener) Close() error {
	if !l.closed.CompareAndSwap(false, true) {
		return nil
	}
	return l.conn.Close()
}

func isTimeoutErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isClosedErr(err error) bool {
	return errors.Is(err, net.ErrClosed)
}
