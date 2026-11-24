package reorder

// Gap describes one unrecoverable run of missing extended sequence numbers.
// Inclusive range [From, To]; adjacent runs are merged into a single gap.
type Gap struct {
	From   uint64
	To     uint64
	Offset int64 // stream offset at which the loss was declared
}

// Length is the number of lost packets in the run.
func (g Gap) Length() uint64 { return g.To - g.From + 1 }

// LossTracker records missing sequence runs and classifies burst losses.
// It never invents packets: callers turn gaps into explicit markers.
type LossTracker struct {
	gaps           []Gap
	missing        uint64
	burstThreshold uint64
	bursts         int
	maxBurst       uint64
}

// NewLossTracker builds a tracker. burstThreshold is the minimum run length
// counted as a burst loss; a value below 2 is raised to 2.
func NewLossTracker(burstThreshold uint64) *LossTracker {
	if burstThreshold < 2 {
		burstThreshold = 2
	}
	return &LossTracker{burstThreshold: burstThreshold}
}

// Record declares every sequence number in [from, to] missing. A run adjacent
// to the previous one extends it instead of opening a new gap.
func (l *LossTracker) Record(from, to uint64, offset int64) Gap {
	if to < from {
		return Gap{}
	}
	if n := len(l.gaps); n > 0 && from == l.gaps[n-1].To+1 {
		l.gaps[n-1].To = to
		l.missing += to - from + 1
		run := l.gaps[n-1].Length()
		if run > l.maxBurst {
			l.maxBurst = run
		}
		l.recomputeBursts()
		return l.gaps[n-1]
	}
	g := Gap{From: from, To: to, Offset: offset}
	l.gaps = append(l.gaps, g)
	l.missing += g.Length()
	if g.Length() > l.maxBurst {
		l.maxBurst = g.Length()
	}
	if g.Length() >= l.burstThreshold {
		l.bursts++
	}
	return g
}

// recomputeBursts refreshes the burst count after a gap was extended.
func (l *LossTracker) recomputeBursts() {
	l.bursts = 0
	for _, g := range l.gaps {
		if g.Length() >= l.burstThreshold {
			l.bursts++
		}
	}
}

// Gaps returns the recorded gaps.
func (l *LossTracker) Gaps() []Gap { return l.gaps }

// Missing returns the total number of lost packets across all gaps.
func (l *LossTracker) Missing() uint64 { return l.missing }

// GapCount returns the number of distinct missing runs.
func (l *LossTracker) GapCount() int { return len(l.gaps) }

// Bursts returns how many runs met the burst-length threshold.
func (l *LossTracker) Bursts() int { return l.bursts }

// MaxBurst returns the longest single loss run observed.
func (l *LossTracker) MaxBurst() uint64 { return l.maxBurst }
