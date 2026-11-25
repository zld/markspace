package kernel

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// SilenceConfig configures PCM silence detection.
type SilenceConfig struct {
	// ThresholdAmplitude is the maximum absolute signed 16-bit sample considered silent.
	ThresholdAmplitude int `json:"threshold_amplitude"`
	// MinDurationMS drops shorter quiet runs.
	MinDurationMS int64 `json:"min_duration_ms"`
}

// DefaultSilenceConfig returns a conservative fixture-friendly threshold.
func DefaultSilenceConfig() SilenceConfig {
	return SilenceConfig{ThresholdAmplitude: 256, MinDurationMS: 500}
}

// SilenceInterval is a gap-free silent interval on the shared clock.
type SilenceInterval struct {
	TrackID        string    `json:"track_id"`
	Time           TimeRange `json:"time"`
	CarvedByGapIDs []string  `json:"carved_by_gap_ids,omitempty"`
}

// DetectSilence finds quiet PCM runs and removes intervals covered by gaps.
func DetectSilence(track *Track, gaps []GapEvent, cfg SilenceConfig) ([]SilenceInterval, error) {
	if track == nil {
		return nil, &MediaError{Stage: "silence", Code: "nil_track", Message: "track is required", Recoverable: false}
	}
	if cfg.ThresholdAmplitude < 0 || cfg.MinDurationMS < 0 {
		return nil, &MediaError{
			Stage:       "silence",
			Code:        "invalid_config",
			Message:     fmt.Sprintf("invalid silence config: %#v", cfg),
			PID:         track.PID,
			Time:        TimeRange{Start90k: track.Start90k, End90k: track.End90k},
			Recoverable: false,
		}
	}
	if cfg.ThresholdAmplitude == 0 {
		cfg.ThresholdAmplitude = DefaultSilenceConfig().ThresholdAmplitude
	}

	type rawRun struct{ r TimeRange }
	var runs []rawRun
	var active *TimeRange
	end := uint64(0)
	have := false

	closeRun := func() {
		if active != nil && active.End90k > active.Start90k {
			runs = append(runs, rawRun{r: *active})
		}
		active = nil
	}

	for _, s := range track.Samples {
		silent := len(s.Data) >= 2 && maxPCM16(s.Data) <= cfg.ThresholdAmplitude
		sStart, sEnd := s.PTS90k, s.PTS90k+s.Duration90k
		if !silent {
			closeRun()
			have = false
			continue
		}
		if active == nil {
			active = &TimeRange{Start90k: sStart, End90k: sEnd}
			end = sEnd
			have = true
			continue
		}
		// Do not bridge a missing sample/time hole; that region is not observed PCM.
		if have && sStart <= end {
			if sEnd > active.End90k {
				active.End90k = sEnd
			}
			end = sEnd
		} else {
			closeRun()
			active = &TimeRange{Start90k: sStart, End90k: sEnd}
			end = sEnd
		}
		have = true
	}
	closeRun()

	minTicks := uint64(cfg.MinDurationMS) * 90
	var out []SilenceInterval
	for _, run := range runs {
		pieces, carved := SubtractGapsWithIDs(run.r, gaps)
		for _, piece := range pieces {
			if piece.Duration90k() < minTicks {
				continue
			}
			out = append(out, SilenceInterval{TrackID: track.ID, Time: piece, CarvedByGapIDs: append([]string(nil), carved...)})
		}
	}
	return out, nil
}

// SilenceTotalMS sums all detected intervals.
func SilenceTotalMS(intervals []SilenceInterval) int64 {
	var ticks uint64
	for _, in := range intervals {
		ticks += in.Time.Duration90k()
	}
	return int64(ticks / 90)
}

func maxPCM16(data []byte) int {
	max := 0
	for i := 0; i+2 <= len(data); i += 2 {
		v := int(int16(binary.LittleEndian.Uint16(data[i : i+2])))
		if v < 0 {
			v = -v
		}
		if v > max {
			max = v
		}
	}
	return max
}

// SubtractGaps removes all gap ranges from r and returns uncovered pieces.
func SubtractGaps(r TimeRange, gaps []GapEvent) []TimeRange {
	pieces, _ := SubtractGapsWithIDs(r, gaps)
	return pieces
}

// SubtractGapsWithIDs also returns IDs of gaps that intersect the source range.
func SubtractGapsWithIDs(r TimeRange, gaps []GapEvent) ([]TimeRange, []string) {
	ordered := append([]GapEvent(nil), gaps...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Time.Start90k < ordered[j].Time.Start90k
	})
	var pieces []TimeRange
	var ids []string
	cur := r.Start90k
	for _, g := range ordered {
		gr := g.Time
		if gr.End90k <= cur {
			continue
		}
		if gr.Start90k >= r.End90k {
			break
		}
		if _, ok := r.Intersect(gr); ok && g.ID != "" {
			ids = append(ids, g.ID)
		}
		gs := max64(cur, gr.Start90k)
		ge := min64(r.End90k, gr.End90k)
		if gs > cur {
			pieces = append(pieces, TimeRange{Start90k: cur, End90k: gs})
		}
		if ge > cur {
			cur = ge
		}
		if cur >= r.End90k {
			break
		}
	}
	if cur < r.End90k {
		pieces = append(pieces, TimeRange{Start90k: cur, End90k: r.End90k})
	}
	return pieces, uniqueStrings(ids)
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
