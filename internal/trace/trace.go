// Package trace synthesises reproducible network delivery traces.
package trace

import (
	"math"
	"math/rand"
	"sort"
	"time"

	"jitterlab/internal/packet"
)

// Config controls trace synthesis. Durations are expressed in milliseconds so
// the same config produces identical traces on every platform.
type Config struct {
	Seed        int64
	Duration    time.Duration
	FrameMS     int
	Loss        float64
	Reorder     float64
	Duplicate   float64
	Burst       int
	JitterMS    int
	ReorderMS   int
	PayloadSize int
	StreamID    uint16
}

// Delivery is one wire packet scheduled to arrive at At.
type Delivery struct {
	At   time.Duration
	Data []byte
}

// Trace is the complete set of arrivals plus generation statistics.
type Trace struct {
	Config      Config
	StreamID    uint16
	FrameMS     time.Duration
	TotalFrames int
	Packets     []Delivery
	Dropped     int
	Duplicated  int
	Delayed     int
}

// DefaultConfig returns the parameters used by the simulate command defaults.
func DefaultConfig() Config {
	return Config{
		Seed:        1,
		Duration:    20 * time.Second,
		FrameMS:     20,
		Loss:        0.08,
		Reorder:     0.12,
		Duplicate:   0.03,
		Burst:       5,
		JitterMS:    12,
		ReorderMS:   90,
		PayloadSize: 160,
		StreamID:    7,
	}
}

// Normalize clamps malformed probabilities and intervals to safe values.
func (c Config) Normalize() Config {
	if c.FrameMS <= 0 {
		c.FrameMS = 20
	}
	if c.Duration <= 0 {
		c.Duration = time.Second
	}
	if c.JitterMS < 0 {
		c.JitterMS = 0
	}
	if c.ReorderMS < 1 {
		c.ReorderMS = 1
	}
	if c.Burst < 1 {
		c.Burst = 1
	}
	if c.PayloadSize < 0 {
		c.PayloadSize = 0
	}
	if c.PayloadSize > packet.MaxPayload {
		c.PayloadSize = packet.MaxPayload
	}
	c.Loss = clamp01(c.Loss)
	c.Reorder = clamp01(c.Reorder)
	c.Duplicate = clamp01(c.Duplicate)
	return c
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Generate builds the trace. Packets are emitted in arrival order, so the
// simulation path feeds the buffered receiver exactly as a socket would.
func Generate(c Config) Trace {
	c = c.Normalize()
	frameMS := time.Duration(c.FrameMS) * time.Millisecond
	total := int(c.Duration / frameMS)
	if total < 1 {
		total = 1
	}

	rng := rand.New(rand.NewSource(c.Seed))
	t := Trace{
		Config:      c,
		StreamID:    c.StreamID,
		FrameMS:     frameMS,
		TotalFrames: total,
	}

	// Loss is budgeted rather than sampled per packet, so the dropped count is
	// deterministic and close to Loss*TotalFrames for every seed. Runs are
	// placed at random but never overlap; this still produces the clumped
	// gaps that stress concealment without making the budget drift.
	// A total loss is handled directly: no packet can be emitted, and the
	// controller must still produce a complete zero-valued report.
	lost := make([]bool, total)
	budget := int(math.Round(c.Loss * float64(total)))
	if c.Loss >= 1 {
		for i := range lost {
			lost[i] = true
		}
		t.Dropped = total
		budget = 0
	}
	placed := 0
	for placed < budget && c.Burst > 0 {
		n := 1 + rng.Intn(c.Burst)
		if placed+n > budget {
			n = budget - placed
		}
		// Try a handful of random windows before falling back to the first
		// free run. Extreme loss rates leave fragmented free space, and
		// failing to place a run must not silently underspend the budget.
		start := -1
		for attempt := 0; attempt < 8; attempt++ {
			cand := rng.Intn(total - n + 1)
			if !overlaps(lost, cand, n) {
				start = cand
				break
			}
		}
		if start < 0 {
			start = firstFree(lost, n)
		}
		if start < 0 {
			// No contiguous free run remains; fill individual free slots so
			// the loss budget is still met exactly.
			for i := 0; i < total && placed < budget; i++ {
				if !lost[i] {
					lost[i] = true
					placed++
				}
			}
			break
		}
		for i := start; i < start+n; i++ {
			lost[i] = true
		}
		placed += n
	}

	payload := make([]byte, c.PayloadSize)

	// Capture timestamps start at a stable non-zero epoch so the controller
	// must normalise them rather than assume they begin at zero.
	const epochMS uint32 = 1_700_000_000_000 & 0xffffffff
	for seq := 0; seq < total; seq++ {
		// Advance the RNG for payload regardless of loss so a config change in
		// loss doesn't reshuffle the payload stream unpredictably.
		for i := range payload {
			payload[i] = byte(rng.Intn(256))
		}
		if len(payload) > 0 {
			payload[0] = byte(seq)
		}

		nominal := time.Duration(seq+1) * frameMS
		jitter := time.Duration(0)
		if c.JitterMS > 0 {
			jitter = time.Duration(rng.Intn(c.JitterMS)) * time.Millisecond
		}
		at := nominal + jitter

		if lost[seq] {
			if c.Loss < 1 {
				t.Dropped++
			}
			continue
		}

		ts := epochMS + uint32(seq*c.FrameMS)
		if rng.Float64() < c.Reorder {
			at += time.Duration(1+rng.Intn(c.ReorderMS)) * time.Millisecond
			t.Delayed++
		}
		p, err := packet.New(c.StreamID, uint32(seq), ts, 1, true, payload)
		if err != nil {
			continue
		}
		wire, err := packet.Marshal(p)
		if err != nil {
			continue
		}
		t.Packets = append(t.Packets, Delivery{At: at, Data: wire})

		if rng.Float64() < c.Duplicate {
			dupAt := at + time.Duration(1+rng.Intn(8))*time.Millisecond
			t.Packets = append(t.Packets, Delivery{At: dupAt, Data: append([]byte(nil), wire...)})
			t.Duplicated++
		}
	}

	sort.SliceStable(t.Packets, func(i, j int) bool { return t.Packets[i].At < t.Packets[j].At })
	return t
}

// FinalArrival returns the timestamp of the last scheduled delivery, or zero
// for an empty trace.
func (t Trace) FinalArrival() time.Duration {
	if len(t.Packets) == 0 {
		return 0
	}
	return t.Packets[len(t.Packets)-1].At
}

func overlaps(lost []bool, start, n int) bool {
	for i := start; i < start+n; i++ {
		if lost[i] {
			return true
		}
	}
	return false
}

func firstFree(lost []bool, n int) int {
	run := 0
	for i := 0; i < len(lost); i++ {
		if lost[i] {
			run = 0
			continue
		}
		run++
		if run == n {
			return i - n + 1
		}
	}
	return -1
}
