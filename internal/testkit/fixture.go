// Package testkit generates deterministic voice packet streams with
// reproducible network impairments. Both the live UDP path and the offline
// replay path consume plans produced here, so a given seed yields the same
// decisions on either path.
package testkit

import (
	"fmt"

	"jitterlab/internal/packet"
)

// MaxFrames caps plan size to bound memory and run time.
const MaxFrames = 200_000

// Fault names a deliberate protocol violation injected into a datagram.
type Fault int

const (
	// FaultNone emits a valid datagram.
	FaultNone Fault = iota
	// FaultChecksum flips a payload byte so the CRC fails.
	FaultChecksum
	// FaultTruncated cuts the datagram short.
	FaultTruncated
	// FaultVersion rewrites the version byte.
	FaultVersion
)

func (f Fault) String() string {
	switch f {
	case FaultNone:
		return "none"
	case FaultChecksum:
		return "checksum"
	case FaultTruncated:
		return "truncated"
	case FaultVersion:
		return "version"
	default:
		return "unknown"
	}
}

// Options describes the stream to generate.
type Options struct {
	Frames    int
	FrameMs   int
	Seed      int64
	Loss      float64
	Reorder   float64
	Duplicate float64

	// BurstStart is the per-frame probability of opening a burst loss
	// window; BurstLen is its length in frames. Loss still applies outside
	// bursts.
	BurstStart float64
	BurstLen   int

	// ExtraDelayMs shifts every arrival later by a fixed amount, modelling
	// path latency. JitterMs adds a uniform random 0..JitterMs on top.
	ExtraDelayMs int
	JitterMs     int

	// ReorderDelay is the maximum additional delay, in frames, applied to
	// reordered packets. DuplicateDelay is the maximum lag of a duplicate.
	ReorderDelay   int
	DuplicateDelay int

	Corrupt    float64
	Truncate   float64
	BadVersion float64

	// DriftPPM skews the receiver's notion of elapsed time.
	DriftPPM int
}

// Validate rejects impossible generation parameters.
func (o Options) Validate() error {
	if o.Frames <= 0 || o.Frames > MaxFrames {
		return fmt.Errorf("testkit: frames %d must be in 1..%d", o.Frames, MaxFrames)
	}
	if o.FrameMs <= 0 || o.FrameMs > 500 {
		return fmt.Errorf("testkit: frame-ms %d must be in 1..500", o.FrameMs)
	}
	for name, p := range map[string]float64{
		"loss": o.Loss, "reorder": o.Reorder, "duplicate": o.Duplicate,
		"burst-start": o.BurstStart, "corrupt": o.Corrupt,
		"truncate": o.Truncate, "bad-version": o.BadVersion,
	} {
		if p < 0 || p > 1 {
			return fmt.Errorf("testkit: %s probability %g must be in 0..1", name, p)
		}
	}
	if o.BurstLen < 0 || o.BurstLen > o.Frames {
		return fmt.Errorf("testkit: burst-len %d out of range", o.BurstLen)
	}
	if o.ExtraDelayMs < 0 || o.ExtraDelayMs > 5000 {
		return fmt.Errorf("testkit: delay-ms %d must be in 0..5000", o.ExtraDelayMs)
	}
	if o.JitterMs < 0 || o.JitterMs > 5000 {
		return fmt.Errorf("testkit: jitter-ms %d must be in 0..5000", o.JitterMs)
	}
	if o.ReorderDelay < 0 || o.ReorderDelay > 256 {
		return fmt.Errorf("testkit: reorder-delay %d out of range", o.ReorderDelay)
	}
	if o.DuplicateDelay < 0 || o.DuplicateDelay > 256 {
		return fmt.Errorf("testkit: duplicate-delay %d out of range", o.DuplicateDelay)
	}
	if o.DriftPPM < -100_000 || o.DriftPPM > 100_000 {
		return fmt.Errorf("testkit: drift-ppm %d out of range", o.DriftPPM)
	}
	return nil
}

// Event is one datagram transmission attempt, or a dropped frame that
// occupies a slot on the sender timeline without being sent.
type Event struct {
	AtMs      int64
	Seq       uint64
	Wire      []byte
	Dropped   bool
	Duplicate bool
	Fault     Fault
}

// Plan is a generated stream plus statistics about the impairments applied.
type Plan struct {
	Options Options
	Events  []Event
	Stats   Stats
}

// Stats counts what generation injected.
type Stats struct {
	Frames     int
	Lost       int
	Reordered  int
	Duplicated int
	Faults     int
	Bursts     int
}

// Generate builds a plan. The same Options always produce identical events.
func Generate(opt Options) (Plan, error) {
	if err := opt.Validate(); err != nil {
		return Plan{}, err
	}
	rng := &rng{state: uint64(opt.Seed)}
	plan := Plan{Options: opt, Stats: Stats{Frames: opt.Frames}}
	burstLeft := 0

	for i := 0; i < opt.Frames; i++ {
		seq := uint64(i)
		at := int64(i) * int64(opt.FrameMs)

		lost := false
		if burstLeft > 0 {
			lost = true
			burstLeft--
		} else if opt.BurstLen > 0 && rng.float() < opt.BurstStart {
			lost = true
			burstLeft = opt.BurstLen - 1
			plan.Stats.Bursts++
		}
		if !lost && rng.float() < opt.Loss {
			lost = true
		}

		frame := packet.Frame{
			Version:    packet.Version,
			Seq:        seq,
			SampleTime: uint32(at),
			SendTime:   uint32(at),
			Payload:    waveform(seq),
		}

		if lost {
			plan.Stats.Lost++
			plan.Events = append(plan.Events, Event{AtMs: at, Seq: seq, Dropped: true})
			continue
		}

		var extra int64
		if opt.JitterMs > 0 {
			extra = int64(rng.intn(opt.JitterMs + 1))
		}
		delay := 0
		if opt.ReorderDelay > 0 && rng.float() < opt.Reorder {
			delay = 1 + rng.intn(opt.ReorderDelay)
			plan.Stats.Reordered++
		}
		arrive := at + int64(opt.ExtraDelayMs) + extra + int64(delay)*int64(opt.FrameMs)
		plan.Events = append(plan.Events, newEvent(arrive, seq, frame, rng, opt, false, &plan.Stats))

		if opt.DuplicateDelay > 0 && rng.float() < opt.Duplicate {
			lag := 1 + rng.intn(opt.DuplicateDelay)
			plan.Stats.Duplicated++
			plan.Events = append(plan.Events, newEvent(arrive+int64(lag)*int64(opt.FrameMs), seq, frame, rng, opt, true, &plan.Stats))
		}
	}
	return plan, nil
}

// newEvent encodes the frame and optionally injects a protocol fault.
func newEvent(at int64, seq uint64, f packet.Frame, rng *rng, opt Options, dup bool, stats *Stats) Event {
	wire := packet.Encode(f)
	fault := FaultNone
	switch {
	case rng.float() < opt.Corrupt:
		fault = FaultChecksum
		wire = append([]byte(nil), wire...)
		wire[packet.HeaderSize] ^= 0xff
	case rng.float() < opt.Truncate:
		fault = FaultTruncated
		wire = wire[:packet.HeaderSize+2]
	case rng.float() < opt.BadVersion:
		fault = FaultVersion
		wire = append([]byte(nil), wire...)
		wire[0] = 9
	}
	if fault != FaultNone {
		stats.Faults++
	}
	return Event{AtMs: at, Seq: seq, Wire: wire, Duplicate: dup, Fault: fault}
}

// StretchMs maps sender time onto the receiver clock after drift. A
// positive ppm makes the receiver clock run fast, so intervals between
// arrivals measure longer and the playout clock has less slack.
func (o Options) StretchMs(atMs int64) int64 {
	return atMs + atMs*int64(o.DriftPPM)/1_000_000
}

// waveform produces a smooth triangle pattern so concealment and recovery
// jumps are audible-sample like rather than random noise.
func waveform(seq uint64) [packet.PayloadSize]byte {
	var p [packet.PayloadSize]byte
	for i := range p {
		v := int((seq*8 + uint64(i)) % 256)
		if v > 127 {
			v = 256 - v
		}
		p[i] = byte(int8(v))
	}
	return p
}

// rng is a SplitMix64 generator. It is implemented locally so plans stay
// byte-for-byte reproducible across Go releases.
type rng struct{ state uint64 }

func (r *rng) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *rng) float() float64 {
	return float64(r.next()>>11) / (1 << 53)
}

func (r *rng) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}
