package mediakernel

// SilenceConfig configures silence detection.
type SilenceConfig struct {
	Threshold   int   // peak absolute amplitude strictly below this is silence
	MinDuration int64 // ns; shorter runs are not reported
	MaxGap      int64 // ns; larger gaps between silent blocks break a run
}

// SilenceInterval is one merged silence run.
type SilenceInterval struct {
	Track uint64 `json:"track"`
	Start int64  `json:"startNs"`
	End   int64  `json:"endNs"`
	Peak  int    `json:"peak"`
}

type silenceRun struct {
	active    bool
	start     int64
	end       int64
	peak      int
	lastEnd   int64
	hasLast   bool
}

// SilenceDetector merges per-block silence decisions into intervals per
// track. A break in the block sequence (contiguous=false) ends the current
// run: interrupted sequences never fabricate continuous silence.
type SilenceDetector struct {
	cfg       SilenceConfig
	runs      map[uint64]*silenceRun
	Intervals []SilenceInterval
}

func NewSilenceDetector(cfg SilenceConfig) *SilenceDetector {
	return &SilenceDetector{cfg: cfg, runs: make(map[uint64]*silenceRun)}
}

// Add feeds one block. peak is the block's peak absolute amplitude.
func (d *SilenceDetector) Add(track uint64, start, dur int64, peak int, contiguous bool) {
	r, ok := d.runs[track]
	if !ok {
		r = &silenceRun{}
		d.runs[track] = r
	}
	silent := peak < d.cfg.Threshold
	if !silent {
		d.flush(track, r)
		r.hasLast, r.lastEnd = true, start+dur
		return
	}
	gap := r.hasLast && start-r.lastEnd > d.cfg.MaxGap
	if !r.active || !contiguous || gap {
		d.flush(track, r)
		r.active, r.start, r.end, r.peak = true, start, start+dur, peak
	} else {
		if start+dur > r.end {
			r.end = start + dur
		}
		if peak > r.peak {
			r.peak = peak
		}
	}
	r.hasLast, r.lastEnd = true, start+dur
}

func (d *SilenceDetector) flush(track uint64, r *silenceRun) {
	if r.active && r.end-r.start >= d.cfg.MinDuration {
		d.Intervals = append(d.Intervals, SilenceInterval{Track: track, Start: r.start, End: r.end, Peak: r.peak})
	}
	r.active = false
}

// Flush terminates all open runs at end of stream.
func (d *SilenceDetector) Flush() {
	for track, r := range d.runs {
		d.flush(track, r)
	}
}

// SilenceState is the serializable detector state for checkpoints.
type SilenceState struct {
	Intervals []SilenceInterval            `json:"intervals"`
	Runs      map[uint64]SilenceInterval   `json:"runs"`
	LastEnd   map[uint64]int64             `json:"lastEnd"`
}

// Snapshot captures the detector state.
func (d *SilenceDetector) Snapshot() SilenceState {
	st := SilenceState{Intervals: d.Intervals,
		Runs: make(map[uint64]SilenceInterval), LastEnd: make(map[uint64]int64)}
	for t, r := range d.runs {
		if r.active {
			st.Runs[t] = SilenceInterval{Track: t, Start: r.start, End: r.end, Peak: r.peak}
		}
		if r.hasLast {
			st.LastEnd[t] = r.lastEnd
		}
	}
	return st
}

// Restore loads a previously snapshotted state.
func (d *SilenceDetector) Restore(st SilenceState) {
	d.Intervals = st.Intervals
	for t, iv := range st.Runs {
		r := d.runs[t]
		if r == nil {
			r = &silenceRun{}
			d.runs[t] = r
		}
		r.active, r.start, r.end, r.peak = true, iv.Start, iv.End, iv.Peak
	}
	for t, le := range st.LastEnd {
		r := d.runs[t]
		if r == nil {
			r = &silenceRun{}
			d.runs[t] = r
		}
		r.hasLast, r.lastEnd = true, le
	}
}
