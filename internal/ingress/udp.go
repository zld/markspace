package ingress

import (
	"errors"
	"net"
	"sync/atomic"
	"time"

	"jitter/internal/buffer"
	"jitter/internal/wire"
)

// UDPReceiver binds a loopback-only UDP socket and feeds decoded packets
// into the shared controller. Malformed datagrams are counted and skipped,
// never fatal.
type UDPReceiver struct {
	conn *net.UDPConn

	malformed atomic.Uint64
}

// ListenUDP binds 127.0.0.1:port (port 0 picks an ephemeral port). It never
// binds external interfaces or dials external targets.
func ListenUDP(port int) (*UDPReceiver, error) {
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	if err := conn.SetReadBuffer(1 << 20); err != nil {
		conn.Close()
		return nil, err
	}
	return &UDPReceiver{conn: conn}, nil
}

// Addr is the bound address, e.g. for senders in tests or demos.
func (r *UDPReceiver) Addr() *net.UDPAddr {
	return r.conn.LocalAddr().(*net.UDPAddr)
}

// Malformed is the count of undecodable datagrams seen so far.
func (r *UDPReceiver) Malformed() uint64 { return r.malformed.Load() }

func (r *UDPReceiver) Close() error { return r.conn.Close() }

// ReadDatagram reads one raw datagram, waiting at most timeout.
func (r *UDPReceiver) ReadDatagram(timeout time.Duration) ([]byte, error) {
	if err := r.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	buf := make([]byte, 1<<16)
	n, _, err := r.conn.ReadFromUDP(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// Serve receives datagrams until stop closes or the socket fails. Decoded
// packets are pushed into ctrl; Push drops on a full queue instead of
// blocking, so the playback clock is never stalled by ingress.
func (r *UDPReceiver) Serve(ctrl *buffer.Controller, stop <-chan struct{}) error {
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		raw, err := r.ReadDatagram(200 * time.Millisecond)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		pkt, err := wire.Decode(raw)
		if err != nil {
			r.malformed.Add(1)
			continue
		}
		ctrl.Push(pkt)
	}
}

// DialLoopback returns a UDP socket connected to addr (must be loopback).
func DialLoopback(addr *net.UDPAddr) (*net.UDPConn, error) {
	if !addr.IP.IsLoopback() {
		return nil, errors.New("ingress: refusing non-loopback target")
	}
	return net.DialUDP("udp", nil, addr)
}

// ExchangeUDP transmits every event's packet over a real loopback UDP
// socket and returns the events in receive order with their original
// schedule. It exercises the same encode/decode path as live reception
// while staying deterministic for the demo.
func ExchangeUDP(evs []Event) ([]Event, error) {
	rx, err := ListenUDP(0)
	if err != nil {
		return nil, err
	}
	defer rx.Close()
	tx, err := DialLoopback(rx.Addr())
	if err != nil {
		return nil, err
	}
	defer tx.Close()

	out := make([]Event, 0, len(evs))
	for _, ev := range evs {
		data, err := wire.Encode(ev.Pkt)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Write(data); err != nil {
			return nil, err
		}
		raw, err := rx.ReadDatagram(time.Second)
		if err != nil {
			return nil, err
		}
		pkt, err := wire.Decode(raw)
		if err != nil {
			rx.malformed.Add(1)
			continue
		}
		out = append(out, Event{At: ev.At, Pkt: pkt})
	}
	return out, nil
}
