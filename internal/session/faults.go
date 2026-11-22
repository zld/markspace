package session

import (
	"math/rand"
)

// Faults is the set of transport faults to inject, one toggle per class.
type Faults struct {
	Duplicate bool // deliver a packet twice
	Reorder   bool // hold a packet back and deliver it after a later one
	BurstLoss bool // drop a run of consecutive packets
	Bandwidth bool // vary inter-packet spacing to shift the rate
	Corrupt   bool // flip a payload byte, breaking the checksum
	Truncated bool // shorten the payload below its declared length
}

// FaultNames lists every supported fault token.
var FaultNames = []string{"duplicate", "reorder", "burst-loss", "bandwidth-shift", "corrupt", "truncated"}

// ParseFaults maps "a,b,c" tokens onto a Faults set.
func ParseFaults(tokens []string) (Faults, error) {
	var f Faults
	for _, tok := range tokens {
		switch tok {
		case "", "none":
		case "duplicate":
			f.Duplicate = true
		case "reorder":
			f.Reorder = true
		case "burst-loss":
			f.BurstLoss = true
		case "bandwidth-shift":
			f.Bandwidth = true
		case "corrupt":
			f.Corrupt = true
		case "truncated":
			f.Truncated = true
		default:
			return f, mediaErr("unknown fault %q (known: %v)", tok, FaultNames)
		}
	}
	return f, nil
}

// Active reports whether any fault class is enabled.
func (f Faults) Active() bool {
	return f.Duplicate || f.Reorder || f.BurstLoss || f.Bandwidth || f.Corrupt || f.Truncated
}

// netPacket is one wire-level packet with its arrival time in nanoseconds.
type netPacket struct {
	raw []byte
	at  int64
}

// FaultConfig tunes injection intensity. Injection points are index-based so
// a given seed always produces the same fault pattern.
type FaultConfig struct {
	DuplicateEvery int
	ReorderEvery   int
	BurstEvery     int
	BurstLen       int
	CorruptEvery   int
	TruncateEvery  int
	RateEvery      int
	GapNs          int64
}

// DefaultFaultConfig produces every fault class the acceptance demo needs
// without starving a track of clean frames.
func DefaultFaultConfig() FaultConfig {
	return FaultConfig{
		DuplicateEvery: 37, ReorderEvery: 29, BurstEvery: 211, BurstLen: 3,
		CorruptEvery: 173, TruncateEvery: 263, RateEvery: 150,
		GapNs: 2_000_000,
	}
}

// injector applies faults to one track's packet stream incrementally. Held
// packets and burst state are bounded by the configuration, never by the
// stream length.
type injector struct {
	rng    *rand.Rand
	faults Faults
	cfg    FaultConfig
	held   []netPacket
	burst  int
	lastAt int64

	Counts FaultCounts
}

func newInjector(seed int64, f Faults, cfg FaultConfig) *injector {
	return &injector{rng: rand.New(rand.NewSource(seed)), faults: f, cfg: cfg}
}

// push feeds packet index i and returns the packets that leave the network,
// already in arrival order. It returns nil when the packet was lost.
func (n *injector) push(raw []byte, i int) []netPacket {
	gap := n.cfg.GapNs
	if gap <= 0 {
		gap = 1
	}
	at := int64(i) * gap
	if n.faults.Bandwidth {
		// Three phases: slightly jittered, a long stall, then a burst. The
		// estimator must retune its window in both directions.
		switch (i / maxInt(1, n.cfg.RateEvery)) % 3 {
		case 0:
			at += int64(i%7) * gap / 8
		case 1:
			at += int64(1+i%3) * gap
		case 2:
			at += int64(i%3) * gap / 5
		}
	}
	if at < n.lastAt {
		at = n.lastAt
	}
	n.lastAt = at
	p := netPacket{raw: raw, at: at}

	if n.faults.BurstLoss && n.cfg.BurstEvery > 0 {
		if n.burst > 0 {
			n.burst--
			n.Counts.Lost++
			return nil
		}
		if i%n.cfg.BurstEvery == n.cfg.BurstEvery-1 {
			n.burst = n.cfg.BurstLen - 1
			n.Counts.Lost++
			return nil
		}
	}
	var out []netPacket
	if n.faults.Duplicate && n.cfg.DuplicateEvery > 0 && i%n.cfg.DuplicateEvery == 5 {
		n.Counts.Duplicated++
		out = append(out, p)
	}
	if n.faults.Corrupt && n.cfg.CorruptEvery > 0 && i%n.cfg.CorruptEvery == 11 {
		n.Counts.Corrupted++
		p.raw = corruptPayload(p.raw)
	}
	if n.faults.Truncated && n.cfg.TruncateEvery > 0 && i%n.cfg.TruncateEvery == 13 {
		n.Counts.Truncated++
		p.raw = truncatePacket(p.raw)
	}
	if n.faults.Reorder && n.cfg.ReorderEvery > 0 && i%n.cfg.ReorderEvery == 7 {
		n.Counts.Reordered++
		n.held = append(n.held, p)
		return out
	}
	out = append(out, p)
	if i%4 == 0 {
		out = append(out, n.held...)
		n.held = n.held[:0]
	}
	return out
}

// flush releases any held packets at end of stream.
func (n *injector) flush() []netPacket {
	out := n.held
	n.held = nil
	return out
}

// FaultCounts is the realized injection tally for one session.
type FaultCounts struct {
	Duplicated int
	Reordered  int
	Lost       int
	Bursts     int
	Corrupted  int
	Truncated  int
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
