package pipeline

import (
	"mediacore/index"
	"mediacore/media"
)

// SilenceState distinguishes a verified quiet interval from one that cannot be
// judged because volume metadata is missing or damaged.
type SilenceState string

const (
	SilenceConfirmed SilenceState = "silence"
	SilenceUnknown   SilenceState = "unknown"
)

// SilenceConfig controls silence detection. Threshold is in dBFS and Duration
// is the minimum continuous quiet time in ticks of the shared media clock.
type SilenceConfig struct {
	ThresholdDBFS    float64
	MinDurationTicks int64
	Timescale        uint32
	// NeighborWindow is how many consecutive frames must agree before an
	// interval is confirmed. Values below two are clamped to two so a single
	// quiet frame cannot trigger a cut.
	NeighborWindow int
}

// DefaultSilenceConfig returns a -45 dBFS, 600 ms policy at the given rate.
func DefaultSilenceConfig(timescale uint32) SilenceConfig {
	clk := media.MustClock(timescale)
	return SilenceConfig{
		ThresholdDBFS:    -45,
		MinDurationTicks: clk.FromMS(600),
		Timescale:        timescale,
		NeighborWindow:   3,
	}
}

// Validate checks the silence policy.
func (c SilenceConfig) Validate() error {
	if c.Timescale == 0 {
		return media.New(media.CatInvalidParams, media.StageSilence, "timescale must be positive")
	}
	if c.ThresholdDBFS > 0 || c.ThresholdDBFS < -200 {
		return media.New(media.CatInvalidParams, media.StageSilence, "threshold %v dBFS out of range", c.ThresholdDBFS)
	}
	if c.MinDurationTicks <= 0 {
		return media.New(media.CatInvalidParams, media.StageSilence, "minimum duration must be positive")
	}
	if c.NeighborWindow < 0 {
		return media.New(media.CatInvalidParams, media.StageSilence, "neighbor window cannot be negative")
	}
	return nil
}

// SilenceInterval is a confirmed or unknown quiet interval. Unknown intervals
// are reported so manifest consumers can see them but never counted as cuts.
type SilenceInterval struct {
	State      SilenceState `json:"state"`
	StartTicks int64        `json:"start_ticks"`
	EndTicks   int64        `json:"end_ticks"`
	StartMS    int64        `json:"start_ms"`
	EndMS      int64        `json:"end_ms"`
	Frames     int          `json:"frames"`
	Reason     string       `json:"reason,omitempty"`
}

// DurationMS is the interval length in milliseconds.
func (s SilenceInterval) DurationMS() int64 { return s.EndMS - s.StartMS }

// Detector finds quiet intervals using per-frame volume summaries and a
// neighbor-agreement window.
type Detector struct {
	cfg SilenceConfig
}

// NewDetector validates cfg.
func NewDetector(cfg SilenceConfig) (*Detector, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.NeighborWindow < 2 {
		cfg.NeighborWindow = 2
	}
	return &Detector{cfg: cfg}, nil
}

// Detect scans entries in index order. A frame whose metadata is absent or
// corrupt breaks any quiet run and is recorded as an unknown interval; it can
// never extend or create a confirmed silence interval.
func (d *Detector) Detect(entries []index.Entry) []SilenceInterval {
	var out []SilenceInterval
	clk := media.MustClock(d.cfg.Timescale)

	type run struct {
		start, end int64
		frames     int
		reason     string
	}
	var quiet *run
	var unknown *run

	closeQuiet := func() {
		if quiet == nil {
			return
		}
		if quiet.frames >= d.cfg.NeighborWindow && quiet.end-quiet.start >= d.cfg.MinDurationTicks {
			out = append(out, SilenceInterval{
				State:      SilenceConfirmed,
				StartTicks: quiet.start,
				EndTicks:   quiet.end,
				StartMS:    clk.ToMS(quiet.start),
				EndMS:      clk.ToMS(quiet.end),
				Frames:     quiet.frames,
			})
		}
		quiet = nil
	}
	closeUnknown := func() {
		if unknown == nil {
			return
		}
		out = append(out, SilenceInterval{
			State:      SilenceUnknown,
			StartTicks: unknown.start,
			EndTicks:   unknown.end,
			StartMS:    clk.ToMS(unknown.start),
			EndMS:      clk.ToMS(unknown.end),
			Frames:     unknown.frames,
			Reason:     unknown.reason,
		})
		unknown = nil
	}

	for i := range entries {
		e := entries[i]
		end := e.Ticks
		if e.DurationTicks > 0 {
			end += int64(e.DurationTicks)
		}
		db, ok := usableVolume(e)
		if !ok {
			closeQuiet()
			reason := "metadata absent"
			if e.MetaStatus == string(media.MetaCorrupt) {
				reason = "metadata corrupt"
			}
			if unknown == nil {
				unknown = &run{start: e.Ticks, reason: reason}
			} else if reason == "metadata corrupt" {
				unknown.reason = reason
			}
			unknown.end = end
			unknown.frames++
			continue
		}
		closeUnknown()
		if db > d.cfg.ThresholdDBFS {
			closeQuiet()
			continue
		}
		if quiet == nil {
			quiet = &run{start: e.Ticks}
		}
		quiet.end = end
		quiet.frames++
	}
	closeQuiet()
	closeUnknown()
	return out
}

func usableVolume(e index.Entry) (float64, bool) {
	if e.MetaStatus != string(media.MetaPresent) || e.VolumeDBFS == nil {
		return 0, false
	}
	return *e.VolumeDBFS, true
}

// Confirmed returns only the intervals safe to use for cutting.
func Confirmed(intervals []SilenceInterval) []SilenceInterval {
	out := make([]SilenceInterval, 0, len(intervals))
	for _, iv := range intervals {
		if iv.State == SilenceConfirmed {
			out = append(out, iv)
		}
	}
	return out
}

// TotalMS sums interval durations in milliseconds for the given state.
func TotalMS(intervals []SilenceInterval, state SilenceState) int64 {
	var total int64
	for _, iv := range intervals {
		if iv.State == state {
			total += iv.DurationMS()
		}
	}
	return total
}
