package kernel

import "fmt"

const (
	// PCRBasePeriod is the 33-bit PCR/PTS wrap period in 90 kHz ticks.
	PCRBasePeriod uint64 = 1 << 33
	// DefaultWrapSlack accepts a forward-around-wrap jump of at most 15 minutes.
	DefaultWrapSlack uint64 = 15 * 60 * ClockHz
)

// ClockState is the checkpointable state of a unified 90 kHz clock.
type ClockState struct {
	LastRaw       uint64 `json:"last_raw"`
	LastUnwrapped uint64 `json:"last_unwrapped"`
	Origin        uint64 `json:"origin"`
	Wraps         int    `json:"wraps"`
	Started       bool   `json:"started"`
}

// Clock unwraps 33-bit MPEG timestamps without rewriting committed values.
type Clock struct {
	pid   uint16
	state ClockState
}

// NewClock creates a clock for the given PCR PID.
func NewClock(pid uint16) *Clock {
	return &Clock{pid: pid}
}

// State returns a copy suitable for a checkpoint.
func (c *Clock) State() ClockState { return c.state }

// RestoreState reloads a checkpoint state.
func (c *Clock) RestoreState(st ClockState) { c.state = st }

// Wraps reports the number of PCR wrap events.
func (c *Clock) Wraps() int { return c.state.Wraps }

// Observe validates raw and returns a monotonic unwrapped timestamp.
//
// A low raw value after a high value is treated as a wrap only when the
// forward-around-wrap distance is small. Other backwards movements are
// timestamp regressions and do not mutate committed state.
func (c *Clock) Observe(raw uint64, packet uint64) (uint64, error) {
	raw &= PCRBasePeriod - 1
	if !c.state.Started {
		c.state.Started = true
		c.state.LastRaw = raw
		c.state.LastUnwrapped = raw
		c.state.Origin = raw
		return raw, nil
	}

	if raw >= c.state.LastRaw {
		v := uint64(c.state.Wraps)*PCRBasePeriod + raw
		if v < c.state.LastUnwrapped {
			return 0, c.regression(raw, packet)
		}
		c.commit(raw, v)
		return v, nil
	}

	forwardAroundWrap := PCRBasePeriod - c.state.LastRaw + raw
	if forwardAroundWrap <= DefaultWrapSlack {
		nextWrap := c.state.Wraps + 1
		v := uint64(nextWrap)*PCRBasePeriod + raw
		if v <= c.state.LastUnwrapped {
			return 0, c.regression(raw, packet)
		}
		c.state.Wraps = nextWrap
		c.commit(raw, v)
		return v, nil
	}

	return 0, c.regression(raw, packet)
}

func (c *Clock) commit(raw, unwrapped uint64) {
	c.state.LastRaw = raw
	c.state.LastUnwrapped = unwrapped
}

func (c *Clock) regression(raw uint64, packet uint64) error {
	return &MediaError{
		Stage:   "clock",
		Code:    "timestamp_regression",
		Message: fmt.Sprintf("raw timestamp moved backwards: raw=%d last_raw=%d", raw, c.state.LastRaw),
		PID:     c.pid,
		Time: TimeRange{
			Start90k: c.state.LastUnwrapped,
			End90k:   c.state.LastUnwrapped + 1,
		},
		Packet:      packet,
		Recoverable: false,
	}
}

// Relative converts an unwrapped timestamp to source-relative media time.
func (c *Clock) Relative(unwrapped uint64) uint64 {
	if unwrapped < c.state.Origin {
		return 0
	}
	return unwrapped - c.state.Origin
}

// ObserveRelative observes raw and returns source-relative media time.
func (c *Clock) ObserveRelative(raw uint64, packet uint64) (uint64, error) {
	v, err := c.Observe(raw, packet)
	if err != nil {
		return 0, err
	}
	return c.Relative(v), nil
}
