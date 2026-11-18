package receiver

import (
	"fmt"
)

// DefaultMaxInvalidRun is the number of consecutive undecodable datagrams
// that terminates a stream when --max-invalid-run is not set.
const DefaultMaxInvalidRun = 16

// ErrStreamDamaged reports that consecutive corruption crossed the
// configured threshold; the stream must be abandoned.
var ErrStreamDamaged = fmt.Errorf("receiver: stream damaged")

// Guard terminates a stream after too many consecutive invalid packets.
// A single valid packet resets the run, so isolated corruption is skipped
// and only a sustained outage of the link or its framing stops the run.
type Guard struct {
	limit   int
	consec  int
	longest int
	Invalid int
}

// NewGuard builds a guard; limit must be positive.
func NewGuard(limit int) (*Guard, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("receiver: max-invalid-run %d must be positive", limit)
	}
	return &Guard{limit: limit}, nil
}

// Observe records the decode result for one datagram. A nil error resets
// the consecutive counter; any error increments it. It returns
// ErrStreamDamaged once the run reaches the limit.
func (g *Guard) Observe(err error) error {
	if err == nil {
		g.consec = 0
		return nil
	}
	g.Invalid++
	g.consec++
	if g.consec > g.longest {
		g.longest = g.consec
	}
	if g.consec >= g.limit {
		return fmt.Errorf("%w: %d consecutive invalid packets", ErrStreamDamaged, g.consec)
	}
	return nil
}

// Consecutive returns the current invalid run length.
func (g *Guard) Consecutive() int { return g.consec }

// Longest returns the longest invalid run observed.
func (g *Guard) Longest() int { return g.longest }
