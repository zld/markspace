package timeline

// TrackKind classifies a track for sync accounting.
type TrackKind int

const (
	KindOther TrackKind = iota
	KindVideo
	KindAudio
)

// RepairConfig controls timestamp repair behaviour.
type RepairConfig struct {
	// WrapThresholdNs: a backward jump at least this large is treated
	// as a counter wrap (muxer restart) rather than a local regression.
	WrapThresholdNs int64
	// MaxSkewNs: maximum allowed audio/video skew after repair. When
	// positive, exceeding it fails with ClassSyncExceeded instead of
	// silently absorbing the drift.
	MaxSkewNs int64
	// MinStepNs: minimum forward step enforced between consecutive
	// repaired timestamps of one track.
	MinStepNs int64
}

// DefaultRepairConfig returns sane defaults for broadcast masters.
func DefaultRepairConfig() RepairConfig {
	return RepairConfig{
		WrapThresholdNs: 5_000_000_000, // 5s backward jump = wrap
		MaxSkewNs:       40_000_000,    // 40ms A/V tolerance
		MinStepNs:       1,             // strictly non-decreasing
	}
}

// RepairStats counts every repair applied, for reproducible reports.
type RepairStats struct {
	Rebased     int   `json:"rebased"`
	Wraps       int   `json:"wraps"`
	Regressions int   `json:"regressions"`
	MaxSkewNs   int64 `json:"max_skew_ns"`
}

type trackState struct {
	seen    bool
	offset  int64 // added to raw ns to obtain repaired ns
	lastAdj int64
}

// Repairer rewrites per-track raw timecode ticks onto the unified
// nanosecond clock: it rebases non-zero starts, unwraps large backward
// jumps and clamps local regressions, keeping per-track output strictly
// monotonic across cluster boundaries.
type Repairer struct {
	clk       Clock
	cfg       RepairConfig
	states    map[uint64]*trackState
	lastVideo int64
	lastAudio int64
	haveVideo bool
	haveAudio bool

	Stats RepairStats
}

// NewRepairer builds a Repairer on the given clock.
func NewRepairer(clk Clock, cfg RepairConfig) *Repairer {
	if cfg.MinStepNs <= 0 {
		cfg.MinStepNs = 1
	}
	return &Repairer{clk: clk, cfg: cfg, states: map[uint64]*trackState{}}
}

// Process converts one raw timecode (in container ticks) for track to
// repaired nanoseconds. offset is the input byte offset, used only for
// fault reporting. kind feeds the A/V skew invariant.
func (r *Repairer) Process(track uint64, kind TrackKind, rawTicks, offset int64) (int64, error) {
	st := r.states[track]
	if st == nil {
		st = &trackState{}
		r.states[track] = st
	}
	rawNs := r.clk.Ns(rawTicks)
	if !st.seen {
		st.seen = true
		if rawNs != 0 {
			st.offset = -rawNs // rebase non-zero start to zero
			r.Stats.Rebased++
		}
	}
	adj := rawNs + st.offset
	if adj < st.lastAdj {
		back := st.lastAdj - adj
		if back >= r.cfg.WrapThresholdNs {
			// Counter wrap / muxer restart: shift the whole track
			// forward so this sample follows the previous one.
			st.offset += back + r.cfg.MinStepNs
			adj = rawNs + st.offset
			r.Stats.Wraps++
		} else {
			// Local regression: clamp forward, never backward.
			adj = st.lastAdj + r.cfg.MinStepNs
			r.Stats.Regressions++
		}
	}
	st.lastAdj = adj
	if err := r.checkSkew(kind, adj, offset); err != nil {
		return adj, err
	}
	return adj, nil
}

// checkSkew enforces the configured A/V skew bound. Skew is measured
// at each video sample against the most recent repaired audio
// timestamp, so interleaving granularity does not masquerade as drift.
func (r *Repairer) checkSkew(kind TrackKind, ns, offset int64) error {
	switch kind {
	case KindVideo:
		r.lastVideo, r.haveVideo = ns, true
	case KindAudio:
		r.lastAudio, r.haveAudio = ns, true
		return nil
	default:
		return nil
	}
	if !r.haveAudio {
		return nil
	}
	skew := r.lastVideo - r.lastAudio
	if skew < 0 {
		skew = -skew
	}
	if skew > r.Stats.MaxSkewNs {
		r.Stats.MaxSkewNs = skew
	}
	if r.cfg.MaxSkewNs > 0 && skew > r.cfg.MaxSkewNs {
		return NewFault(ClassSyncExceeded, offset, 0,
			"A/V skew %dns exceeds limit %dns", skew, r.cfg.MaxSkewNs)
	}
	return nil
}
