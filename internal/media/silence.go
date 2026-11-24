package media

import "time"

// SilenceConfig controls energy-based silence detection.
type SilenceConfig struct {
	// EnergyThreshold classifies a frame as low energy when Energy <= it.
	EnergyThreshold int
	// MinDuration is the minimum continuous low-energy duration reported.
	MinDuration time.Duration
}

// SilenceRange is written to the manifest using nanoseconds on clock.TimeBase.
type SilenceRange struct {
	StartNS       int64  `json:"start_ns"`
	EndNS         int64  `json:"end_ns"`
	DurationNS    int64  `json:"duration_ns"`
	StartSequence uint16 `json:"start_sequence"`
	EndSequence   uint16 `json:"end_sequence"`
	FrameCount    int    `json:"frame_count"`
}

// SilenceDetector groups consecutive low-energy frames. Runs shorter than
// MinDuration are suppressed and never produce a cut/boundary.
type SilenceDetector struct {
	cfg          SilenceConfig
	run          []Frame
	shortRuns    int
	suppressedNS time.Duration
}

func NewSilenceDetector(cfg SilenceConfig) *SilenceDetector {
	if cfg.EnergyThreshold == 0 {
		cfg.EnergyThreshold = 500
	}
	if cfg.MinDuration == 0 {
		cfg.MinDuration = 500 * time.Millisecond
	}
	return &SilenceDetector{cfg: cfg}
}

// Add consumes one valid frame. It returns a completed SilenceRange when a
// low-energy run reaches the configured minimum duration and a louder frame
// closes it. The returned range is nil for short runs and while a run is open.
func (d *SilenceDetector) Add(f Frame) *SilenceRange {
	low := f.Energy <= d.cfg.EnergyThreshold
	if low {
		d.run = append(d.run, f)
		return nil
	}
	if len(d.run) == 0 {
		return nil
	}
	r := d.finish()
	d.run = nil
	return r
}

// Flush closes a trailing low-energy run. It returns nil for a short run.
func (d *SilenceDetector) Flush() *SilenceRange {
	if len(d.run) == 0 {
		return nil
	}
	r := d.finish()
	d.run = nil
	return r
}

func (d *SilenceDetector) finish() *SilenceRange {
	if len(d.run) == 0 {
		return nil
	}
	first := d.run[0]
	last := d.run[len(d.run)-1]
	start := first.PTS
	end := last.End()
	duration := end - start
	if duration < d.cfg.MinDuration {
		d.shortRuns++
		d.suppressedNS += duration
		return nil
	}
	return &SilenceRange{
		StartNS:       int64(start),
		EndNS:         int64(end),
		DurationNS:    int64(duration),
		StartSequence: first.Sequence,
		EndSequence:   last.Sequence,
		FrameCount:    len(d.run),
	}
}

func (d *SilenceDetector) ShortRuns() int { return d.shortRuns }

// SilenceState is the checkpoint state for an open run and suppressed runs.
type SilenceState struct {
	Run          []FrameState `json:"run,omitempty"`
	ShortRuns    int          `json:"short_runs"`
	SuppressedNS int64        `json:"suppressed_ns"`
}

func (d *SilenceDetector) State() SilenceState {
	st := SilenceState{ShortRuns: d.shortRuns, SuppressedNS: int64(d.suppressedNS)}
	for _, f := range d.run {
		st.Run = append(st.Run, f.state())
	}
	return st
}

func (d *SilenceDetector) Restore(st SilenceState) {
	d.shortRuns = st.ShortRuns
	d.suppressedNS = time.Duration(st.SuppressedNS)
	d.run = d.run[:0]
	for _, fs := range st.Run {
		d.run = append(d.run, frameFromState(fs))
	}
}
