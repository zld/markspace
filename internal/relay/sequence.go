// Package relay forwards RTP packets between the two loopback connections
// and validates their sequencing.
package relay

import (
	"sync"
)

// Checker validates RTP sequence numbers: it detects gaps (lost packets),
// duplicates and out-of-order arrivals. Sequence numbers are uint16 and
// wrap around per RFC 3550.
type Checker struct {
	mu        sync.Mutex
	started   bool
	maxSeq    uint16
	seen      map[uint16]struct{}
	received  uint64 // unique packets
	duplicate uint64
	reordered uint64
}

// NewChecker creates an empty Checker.
func NewChecker() *Checker {
	return &Checker{seen: make(map[uint16]struct{})}
}

// Check records one arrived sequence number and reports whether it is a
// duplicate or an out-of-order (reordered) packet.
func (c *Checker) Check(seq uint16) (duplicate, reordered bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		c.started = true
		c.maxSeq = seq
		c.seen[seq] = struct{}{}
		c.received++
		return false, false
	}
	if _, ok := c.seen[seq]; ok {
		c.duplicate++
		return true, false
	}
	c.seen[seq] = struct{}{}
	c.received++
	// Ahead of the highest sequence seen (with wrap): in-order arrival.
	if int16(seq-c.maxSeq) > 0 {
		c.maxSeq = seq
		return false, false
	}
	c.reordered++
	return false, true
}

// Counts is a consistent snapshot of the Checker.
type Counts struct {
	Received  uint64
	Duplicate uint64
	Reordered uint64
}

// Snapshot returns the current counters.
func (c *Checker) Snapshot() Counts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Counts{Received: c.received, Duplicate: c.duplicate, Reordered: c.reordered}
}

// Lost returns how many of the expected packets never arrived.
func (c *Checker) Lost(expected uint64) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.received >= expected {
		return 0
	}
	return expected - c.received
}

// ExpectedPayload builds the deterministic payload the generator puts into
// the packet with the given sequence number. The forwarder uses it to
// verify forwarded content byte for byte.
func ExpectedPayload(seq uint16, size int) []byte {
	buf := make([]byte, size)
	seed := byte(seq>>8) ^ byte(seq)
	for i := range buf {
		buf[i] = seed ^ byte(i*31+7)
	}
	return buf
}
