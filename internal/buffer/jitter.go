// Package buffer implements the media jitter buffer. Units are held for a
// bounded timeline window; when the window expires the buffer releases what
// it has and reports every missing run of sequence numbers as an explicit gap
// record anchored between the neighbouring timeline positions. Downstream
// stages therefore never see a silent hole.
package buffer

import (
	"sort"

	"example.com/mediaforge/internal/merrors"
)

// Unit is one buffered media unit. Extended is its position on the shared
// integer timeline; Seq is the RTP sequence number used to detect gaps.
type Unit struct {
	SSRC      uint32
	Seq       uint16
	Timestamp uint32 // raw RTP timestamp
	Extended  uint64 // extended timeline position, ticks
	Marker    bool
	Payload   []byte
	// Arrival is the reference arrival time in ticks of the shared clock.
	Arrival uint64
}

// Config bounds the buffer.
type Config struct {
	// WindowTicks is how far the buffer must run ahead of a unit before that
	// unit is considered final and released.
	WindowTicks uint64
	// MaxUnits bounds memory; a push beyond it is refused.
	MaxUnits int
	// HighWater releases the oldest unit early once this many units are
	// held, so a persistent hole cannot stall delivery.
	HighWater int
}

// DefaultConfig returns sane defaults for 90 kHz video.
func DefaultConfig() Config {
	return Config{WindowTicks: 9000, MaxUnits: 8192, HighWater: 4096}
}

func (c Config) validate() error {
	if c.WindowTicks == 0 {
		return merrors.New(merrors.StageJitter, merrors.Invalid, "window must be positive")
	}
	if c.MaxUnits <= 0 || c.MaxUnits > 1<<20 {
		return merrors.New(merrors.StageJitter, merrors.Invalid,
			"max units %d outside 1..1048576", c.MaxUnits)
	}
	if c.HighWater <= 0 || c.HighWater > c.MaxUnits {
		return merrors.New(merrors.StageJitter, merrors.Invalid,
			"high water %d outside 1..%d", c.HighWater, c.MaxUnits)
	}
	return nil
}

// Gap describes a run of missing sequence numbers and the timeline region it
// leaves empty. After/Before are the timeline positions of the units on each
// side; HasAfter is false for a gap before the first unit of the stream.
type Gap struct {
	SSRC       uint32
	FromSeq    uint16
	Count      int
	AfterTick  uint64
	HasAfter   bool
	BeforeTick uint64
	HasBefore  bool
	Final      bool
}

// Stats counts buffer activity.
type Stats struct {
	Pushed   int
	Released int
	Gaps     int
	Dropped  int
	MaxDepth int
}

// JitterBuffer holds units for one SSRC.
type JitterBuffer struct {
	cfg  Config
	ssrc uint32

	units []Unit // sorted by Extended, then Seq

	started  bool
	havePrev bool
	prev     Unit // last released unit

	st Stats
}

// New creates a jitter buffer for ssrc.
func New(ssrc uint32, cfg Config) (*JitterBuffer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &JitterBuffer{cfg: cfg, ssrc: ssrc}, nil
}

// Stats returns counters.
func (j *JitterBuffer) Stats() Stats { return j.st }

// Depth returns the number of buffered units.
func (j *JitterBuffer) Depth() int { return len(j.units) }

// seqDelta returns b-a as a signed 16-bit delta.
func seqDelta(b, a uint16) int { return int(int16(b - a)) }

func less(a, b Unit) bool {
	if a.Extended != b.Extended {
		return a.Extended < b.Extended
	}
	return seqDelta(a.Seq, b.Seq) < 0
}

// Push inserts a unit. A unit that can never be ordered behind the already
// released data is refused as late; an exact duplicate is refused as a
// duplicate. Both are reported, never silently dropped.
func (j *JitterBuffer) Push(u Unit) error {
	if u.SSRC != j.ssrc {
		return merrors.Packet(merrors.StageJitter, merrors.Invalid, u.SSRC, u.Seq,
			"unit for unexpected SSRC")
	}
	if len(j.units) >= j.cfg.MaxUnits {
		return merrors.Packet(merrors.StageJitter, merrors.Overflow, u.SSRC, u.Seq,
			"buffer full (%d units)", len(j.units))
	}
	i := sort.Search(len(j.units), func(k int) bool { return !less(j.units[k], u) })
	if i < len(j.units) && j.units[i].Extended == u.Extended && j.units[i].Seq == u.Seq {
		j.st.Dropped++
		return merrors.Packet(merrors.StageJitter, merrors.Duplicate, u.SSRC, u.Seq,
			"duplicate unit at tick %d", u.Extended)
	}
	// Once the buffer has released its window, a packet whose sequence the
	// released data already passed can never be placed in order.
	if j.havePrev && seqDelta(u.Seq, j.prev.Seq) <= 0 && len(j.units) == 0 {
		j.st.Dropped++
		return merrors.Packet(merrors.StageJitter, merrors.Late, u.SSRC, u.Seq,
			"unit precedes released sequence %d", j.prev.Seq)
	}
	j.units = append(j.units, Unit{})
	copy(j.units[i+1:], j.units[i:])
	j.units[i] = u
	j.st.Pushed++
	if len(j.units) > j.st.MaxDepth {
		j.st.MaxDepth = len(j.units)
	}
	return nil
}

// Ready releases every unit that is at least WindowTicks behind the newest
// buffered position, in timeline order, and reports each missing sequence run
// between released units. It is safe to call after every Push.
func (j *JitterBuffer) Ready() (units []Unit, gaps []Gap) {
	if len(j.units) == 0 {
		return nil, nil
	}
	edge := j.units[len(j.units)-1].Extended
	for len(j.units) > 0 {
		next := j.units[0]
		expired := edge-next.Extended >= j.cfg.WindowTicks
		if !expired && len(j.units) <= j.cfg.HighWater {
			break
		}
		if g, ok := j.releaseOne(next); ok {
			gaps = append(gaps, g)
		}
		units = append(units, next)
		j.units = j.units[1:]
	}
	return units, gaps
}

// Flush releases everything held, reporting the holes between units. It is
// used at end of stream.
func (j *JitterBuffer) Flush() (units []Unit, gaps []Gap) {
	for len(j.units) > 0 {
		next := j.units[0]
		if g, ok := j.releaseOne(next); ok {
			gaps = append(gaps, g)
		}
		units = append(units, next)
		j.units = j.units[1:]
	}
	return units, gaps
}

// releaseOne accounts for a unit being finalised and returns the gap that
// preceded it, if the sequence numbers are not contiguous.
func (j *JitterBuffer) releaseOne(u Unit) (Gap, bool) {
	var g Gap
	hasGap := false
	if j.havePrev {
		d := seqDelta(u.Seq, j.prev.Seq)
		if d > 1 {
			g = Gap{
				SSRC:       j.ssrc,
				FromSeq:    j.prev.Seq + 1,
				Count:      d - 1,
				AfterTick:  j.prev.Extended,
				HasAfter:   true,
				BeforeTick: u.Extended,
				HasBefore:  true,
				Final:      true,
			}
			hasGap = true
			j.st.Gaps++
		}
	}
	j.prev = u
	j.havePrev = true
	j.st.Released++
	return g, hasGap
}

// Frontier returns the extended timestamp of the last released unit.
func (j *JitterBuffer) Frontier() (uint64, bool) {
	if !j.havePrev {
		return 0, false
	}
	return j.prev.Extended, true
}
