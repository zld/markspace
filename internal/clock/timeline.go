// Package clock maps per-track media timescales onto a unified RTP
// timestamp base, so fragments from tracks with different timescales
// (e.g. 90000 video, 48000 audio) can be compared on one axis.
package clock

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pion/rtp"

	"replay/internal/model"
)

// markerPayloader emits the payload as a single RTP chunk. The payload is a
// placeholder: only the RTP timestamp accounting matters here.
type markerPayloader struct{}

func (markerPayloader) Payload(_ uint16, payload []byte) [][]byte { return [][]byte{payload} }

// Clock converts media sample counts in one track's timescale into RTP
// timestamps via an rtp.Packetizer, which handles 32-bit wraparound the
// same way a live RTP sender would.
type Clock struct {
	rate uint32
	pz   rtp.Packetizer
}

// New builds a Clock for the given media timescale (e.g. 90000, 48000).
func New(rate uint32, ssrc uint32) (*Clock, error) {
	if rate == 0 {
		return nil, errors.New("clock: clock rate must be positive")
	}
	return &Clock{
		rate: rate,
		pz:   rtp.NewPacketizer(1200, 96, ssrc, markerPayloader{}, rtp.NewFixedSequencer(0), rate),
	}, nil
}

// Rate returns the media timescale this clock runs at.
func (c *Clock) Rate() uint32 { return c.rate }

// Advance records samples media samples (in Rate units) and returns the RTP
// timestamp at which the interval starts. The timestamp wraps at 2^32 like
// a real RTP clock; callers compare positions via durations, not raw values.
func (c *Clock) Advance(samples uint32) uint32 {
	pkts := c.pz.Packetize([]byte{0x01}, samples)
	if len(pkts) == 0 {
		return 0 // unreachable: payload is non-empty
	}
	return pkts[0].Header.Timestamp
}

// ToDuration converts media-clock units to a Duration, rejecting values
// that would overflow int64 nanoseconds.
func ToDuration(units uint64, rate uint32) (time.Duration, error) {
	if rate == 0 {
		return 0, errors.New("clock: zero rate")
	}
	r := uint64(rate)
	sec := units / r
	rem := units % r
	const nsPerSec = uint64(time.Second)
	maxSec := uint64(math.MaxInt64) / nsPerSec
	if sec > maxSec {
		return 0, fmt.Errorf("clock: %d units at rate %d: %w", units, rate, model.ErrOverflow)
	}
	// rem < r <= 2^32 so rem*1e9 stays below 2^64.
	remNanos := rem * nsPerSec / r
	secNanos := sec * nsPerSec // safe: sec <= maxSec
	if sec == maxSec && remNanos > uint64(math.MaxInt64)-secNanos {
		return 0, fmt.Errorf("clock: %d units at rate %d: %w", units, rate, model.ErrOverflow)
	}
	return time.Duration(secNanos) + time.Duration(remNanos), nil
}

// FromDuration converts a Duration to media-clock units, rejecting negative
// durations and overflows.
func FromDuration(d time.Duration, rate uint32) (uint64, error) {
	if rate == 0 {
		return 0, errors.New("clock: zero rate")
	}
	if d < 0 {
		return 0, fmt.Errorf("clock: negative duration %v: %w", d, model.ErrOverflow)
	}
	if uint64(d) > math.MaxUint64/uint64(rate) {
		return 0, fmt.Errorf("clock: %v at rate %d: %w", d, rate, model.ErrOverflow)
	}
	return uint64(d) * uint64(rate) / uint64(time.Second), nil
}
