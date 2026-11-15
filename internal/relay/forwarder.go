// Package relay forwards RTP packets across a loopback UDP hop and keeps
// loss, duplication, and reordering statistics.
package relay

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"

	"relaycheck/internal/metrics"
	"relaycheck/internal/transport"
)

// Stats is a point-in-time snapshot of forwarder counters.
type Stats struct {
	Forwarded  uint64
	Dropped    uint64
	Duplicates uint64
	Reordered  uint64
	WriteErrs  uint64
	Received   uint64
}

// Forwarder receives RTP packets from an upstream (the WebRTC track),
// writes them onto a loopback UDP socket, and reads them back on a bound
// listener where sequence and jitter statistics are collected.
type Forwarder struct {
	listener *net.UDPConn
	writer   *net.UDPConn
	queue    chan []byte
	tracker  *SequenceTracker
	jitter   *metrics.Jitter

	closed atomic.Bool
	wg     sync.WaitGroup

	mu        sync.Mutex
	forwarded uint64
	dropped   uint64
	writeErrs uint64
	received  uint64
}

// NewForwarder binds a loopback UDP listener on ip:port (port 0 picks a
// random free port) and starts the worker and reader goroutines.
func NewForwarder(ip string, port, queueSize int, clockRate uint32) (*Forwarder, error) {
	listener, err := transport.BindLoopbackUDP(ip, port)
	if err != nil {
		return nil, err
	}
	dst := listener.LocalAddr().(*net.UDPAddr)
	writer, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		listener.Close()
		return nil, err
	}
	if queueSize < 1 {
		queueSize = 1
	}
	f := &Forwarder{
		listener: listener,
		writer:   writer,
		queue:    make(chan []byte, queueSize),
		tracker:  NewSequenceTracker(),
		jitter:   metrics.NewJitter(clockRate),
	}
	f.wg.Add(2)
	go f.writeLoop()
	go f.readLoop()
	return f, nil
}

// Addr returns the bound listener address (useful with a random port).
func (f *Forwarder) Addr() string { return f.listener.LocalAddr().String() }

// Submit marshals one RTP packet and queues it for forwarding. Packets are
// dropped (and counted) when the queue is full or the forwarder is closed.
func (f *Forwarder) Submit(pkt *rtp.Packet) {
	if f.closed.Load() {
		f.mu.Lock()
		f.dropped++
		f.mu.Unlock()
		return
	}
	raw, err := pkt.Marshal()
	if err != nil {
		f.mu.Lock()
		f.dropped++
		f.mu.Unlock()
		return
	}
	select {
	case f.queue <- raw:
	default:
		f.mu.Lock()
		f.dropped++
		f.mu.Unlock()
	}
}

func (f *Forwarder) writeLoop() {
	defer f.wg.Done()
	for raw := range f.queue {
		if _, err := f.writer.Write(raw); err != nil {
			f.mu.Lock()
			f.writeErrs++
			f.mu.Unlock()
			continue
		}
		f.mu.Lock()
		f.forwarded++
		f.mu.Unlock()
	}
}

func (f *Forwarder) readLoop() {
	defer f.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, _, err := f.listener.ReadFromUDP(buf)
		if err != nil {
			return // listener closed by Close
		}
		arrival := time.Now()
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}
		f.tracker.Observe(pkt.SequenceNumber)
		f.jitter.Observe(pkt.Timestamp, arrival)
		f.mu.Lock()
		f.received++
		f.mu.Unlock()
	}
}

// Close stops accepting packets, drains the queue, closes the sockets, and
// waits for the worker goroutines to exit.
func (f *Forwarder) Close() error {
	if f.closed.Swap(true) {
		return nil
	}
	close(f.queue) // lets writeLoop drain and exit
	f.writer.Close()
	f.listener.Close() // unblocks readLoop
	f.wg.Wait()
	return nil
}

// Stats returns a consistent snapshot of all counters.
func (f *Forwarder) Stats() Stats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Stats{
		Forwarded:  f.forwarded,
		Dropped:    f.dropped,
		Duplicates: f.tracker.Duplicates(),
		Reordered:  f.tracker.Reordered(),
		WriteErrs:  f.writeErrs,
		Received:   f.received,
	}
}

// Jitter returns the arrival-jitter estimator of the receive path.
func (f *Forwarder) Jitter() *metrics.Jitter { return f.jitter }

// ErrIncomplete is returned by callers when received != expected.
var ErrIncomplete = errors.New("forwarding incomplete")
