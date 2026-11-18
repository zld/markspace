// Package netem provides loopback-only UDP transport and deterministic
// impairment injection. Nothing here inspects real network traffic: the
// sender and receiver are both created by this process on 127.0.0.1.
package netem

import (
	"fmt"
	"net"
	"time"

	"jitterlab/internal/testkit"
)

// Loopback is the only address this tool binds to.
const Loopback = "127.0.0.1"

// Receiver owns the listening UDP socket.
type Receiver struct {
	conn *net.UDPConn
}

// Listen opens a loopback UDP socket. Port 0 asks the kernel to choose.
func Listen(port int) (*Receiver, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("netem: port %d out of range", port)
	}
	addr := &net.UDPAddr{IP: net.ParseIP(Loopback), Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("netem: listen %s: %w", addr, err)
	}
	return &Receiver{conn: conn}, nil
}

// Port reports the bound UDP port.
func (r *Receiver) Port() int { return r.conn.LocalAddr().(*net.UDPAddr).Port }

// Close releases the socket. It is safe to call more than once.
func (r *Receiver) Close() error {
	return r.conn.Close()
}

// Datagram is one received payload with the wall-clock time of receipt.
type Datagram struct {
	Data []byte
	At   time.Time
}

// Read reads one datagram into a bounded buffer. Read deadline errors are
// returned as-is so the caller can distinguish timeouts from damage.
func (r *Receiver) Read(buf []byte, timeout time.Duration) (Datagram, error) {
	if err := r.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return Datagram{}, err
	}
	n, err := r.conn.Read(buf)
	if err != nil {
		return Datagram{}, err
	}
	out := make([]byte, n)
	copy(out, buf[:n])
	return Datagram{Data: out, At: time.Now()}, nil
}

// Sender transmits generated events to a loopback receiver.
type Sender struct {
	conn *net.UDPConn
	dst  *net.UDPAddr
}

// Dial opens a connected loopback UDP socket aimed at port.
func Dial(port int) (*Sender, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("netem: destination port %d out of range", port)
	}
	dst := &net.UDPAddr{IP: net.ParseIP(Loopback), Port: port}
	conn, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		return nil, fmt.Errorf("netem: dial %s: %w", dst, err)
	}
	return &Sender{conn: conn, dst: dst}, nil
}

// Close releases the socket.
func (s *Sender) Close() error { return s.conn.Close() }

// Send writes one datagram immediately.
func (s *Sender) Send(data []byte) error {
	if len(data) > 64*1024 {
		return fmt.Errorf("netem: datagram of %d bytes too large", len(data))
	}
	if _, err := s.conn.Write(data); err != nil {
		return fmt.Errorf("netem: send: %w", err)
	}
	return nil
}

// SendStats reports what the sender actually put on the wire.
type SendStats struct {
	Sent   int
	Lost   int
	Failed int
	Faults int
}

// PlayPlan transmits a plan in real time relative to the current clock.
// Events marked Dropped are skipped (they model loss before transmission),
// and events are paced by their AtMs offsets so the receiver sees the
// intended jitter. The returned stats allow the live path to reconcile
// input counts against the plan.
func PlayPlan(s *Sender, plan testkit.Plan, stop <-chan struct{}) (SendStats, error) {
	var stats SendStats
	start := time.Now()
	for _, ev := range plan.Events {
		if ev.Dropped {
			stats.Lost++
			continue
		}
		at := plan.Options.StretchMs(ev.AtMs)
		wait := time.Until(start.Add(time.Duration(at) * time.Millisecond))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-stop:
				timer.Stop()
				return stats, nil
			}
		}
		if ev.Fault != testkit.FaultNone {
			stats.Faults++
		}
		if err := s.Send(ev.Wire); err != nil {
			stats.Failed++
			return stats, err
		}
		stats.Sent++
	}
	return stats, nil
}
