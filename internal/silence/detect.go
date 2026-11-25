// Package silence detects silent intervals from decoded PCM audio samples on
// the common microsecond axis. It works on raw sample amplitudes (no codec
// dependency) so fixtures can synthesise PCM directly.
package silence

import (
	"math"

	"mediad/internal/splitter"
)

// Config parameterises silence detection.
type Config struct {
	// AmplitudeThreshold is the linear peak level (0..1) at or below which a
	// sample counts as silent.
	AmplitudeThreshold float64
	// MinDurationUS is the shortest silence interval worth reporting.
	MinDurationUS int64
}

// Interval is one reported silence region on the common axis.
type Interval struct {
	TrackID uint32  `json:"track_id"`
	StartUS int64   `json:"start_us"`
	EndUS   int64   `json:"end_us"`
	Peak    float64 `json:"peak"`
	RMS     float64 `json:"rms"`
}

// PCMFrame is one decoded audio frame with its location on the common axis.
// Samples are signed linear levels already normalised to [-1,1].
type PCMFrame struct {
	TrackID uint32
	StartUS int64
	EndUS   int64
	Samples []float64
	// Invalid mirrors splitter invalid intervals; silence never spans them.
	Invalid bool
}

// accumulator holds the running level statistics of one open interval.
type accumulator struct {
	trackID uint32
	startUS int64
	endUS   int64
	peak    float64
	sumSq   float64
	count   int
}

func (a *accumulator) add(f PCMFrame) {
	for _, v := range f.Samples {
		if x := math.Abs(v); x > a.peak {
			a.peak = x
		}
		a.sumSq += v * v
		a.count++
	}
	a.endUS = f.EndUS
}

func (a *accumulator) rms() float64 {
	if a.count == 0 {
		return 0
	}
	return math.Sqrt(a.sumSq / float64(a.count))
}

// peakRMS returns the peak absolute amplitude and root-mean-square level.
func peakRMS(x []float64) (peak, rms float64) {
	var sumSq float64
	for _, v := range x {
		a := math.Abs(v)
		if a > peak {
			peak = a
		}
		sumSq += v * v
	}
	if len(x) > 0 {
		rms = math.Sqrt(sumSq / float64(len(x)))
	}
	return peak, rms
}

// Detect scans decoded frames in time order and emits silence intervals whose
// peak stays at or below the threshold and whose duration meets the minimum.
// An invalid or loud frame always terminates any open silence interval.
func Detect(frames []PCMFrame, cfg Config) []Interval {
	var out []Interval
	var acc *accumulator
	flush := func() {
		if acc == nil {
			return
		}
		if acc.endUS-acc.startUS >= cfg.MinDurationUS {
			out = append(out, Interval{
				TrackID: acc.trackID, StartUS: acc.startUS, EndUS: acc.endUS,
				Peak: acc.peak, RMS: acc.rms(),
			})
		}
		acc = nil
	}

	for _, f := range frames {
		peak, _ := peakRMS(f.Samples)
		silent := !f.Invalid && peak <= cfg.AmplitudeThreshold
		if !silent {
			flush()
			continue
		}
		if acc == nil {
			acc = &accumulator{trackID: f.TrackID, startUS: f.StartUS}
		}
		acc.add(f)
	}
	flush()
	return out
}

// DetectFromMapped builds PCM frames from mapped samples and their amplitudes.
// The amplitudes map is keyed by sample index and must align with samples.
func DetectFromMapped(samples []splitter.MappedSample, amplitudes map[int][]float64, cfg Config) []Interval {
	frames := make([]PCMFrame, 0, len(samples))
	for _, s := range samples {
		frames = append(frames, PCMFrame{
			TrackID: s.TrackID, StartUS: s.StartUS, EndUS: s.EndUS,
			Samples: amplitudes[s.Index], Invalid: s.Invalid,
		})
	}
	return Detect(frames, cfg)
}
