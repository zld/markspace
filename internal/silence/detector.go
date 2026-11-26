// Package silence detects sustained low-RMS audio windows while explicitly
// excluding packet-loss and corruption intervals.
package silence

import (
	"math"

	"pwflow/internal/timebase"
)

const (
	DefaultThresholdDBFS = -55.0
	DefaultMinDurationNS = 300 * 1_000_000
	DefaultWindowNS      = 20 * 1_000_000
)

// Options configures RMS windowing and the silence persistence rule.
type Options struct {
	SampleRate    int
	ThresholdDBFS float64
	MinDurationNS int64
	WindowNS      int64
	Exclusions    []timebase.Interval
}

// Window is one analyzed RMS window.
type Window struct {
	timebase.Interval
	RMSDBFS  float64 `json:"rms_dbfs"`
	Excluded bool    `json:"excluded"`
	Silent   bool    `json:"silent"`
}

// Report contains silence ranges and loudness measurements.
type Report struct {
	Silences      []timebase.Interval `json:"silence_intervals"`
	Windows       []Window            `json:"-"`
	PeakDBFS      *float64            `json:"peak_dbfs"`
	MeanRMSDBFS   *float64            `json:"mean_rms_dbfs"`
	TotalSamples  int                 `json:"total_samples"`
	ExcludedCount int                 `json:"excluded_windows"`
}

// DetectS16LE analyzes signed 16-bit little-endian mono PCM. Exclusions use
// the common nanosecond timeline. A window intersecting an exclusion is
// never classified as silence and breaks a consecutive silence run.
func DetectS16LE(pcm []byte, opts Options) Report {
	if opts.SampleRate <= 0 {
		opts.SampleRate = 48000
	}
	if opts.ThresholdDBFS == 0 {
		opts.ThresholdDBFS = DefaultThresholdDBFS
	}
	if opts.MinDurationNS <= 0 {
		opts.MinDurationNS = DefaultMinDurationNS
	}
	if opts.WindowNS <= 0 {
		opts.WindowNS = DefaultWindowNS
	}
	windowSamples := int((int64(opts.SampleRate) * opts.WindowNS) / timebase.Seconds(1))
	if windowSamples < 1 {
		windowSamples = 1
	}
	thresholdRMS := math.Pow(10, opts.ThresholdDBFS/20.0) * 32768.0
	exclusions := timebase.MergeOverlapping(opts.Exclusions)

	rep := Report{}
	var peak int
	var sumSquares float64
	var totalSamples int
	var runStart, runEnd int64
	haveRun := false
	closeRun := func() {
		if haveRun && runEnd-runStart >= opts.MinDurationNS {
			rep.Silences = append(rep.Silences, timebase.Interval{Lo: runStart, Hi: runEnd})
		}
		haveRun = false
	}

	for off := 0; off+1 < len(pcm); {
		endByte := off + windowSamples*2
		if endByte > len(pcm)-len(pcm)%2 {
			endByte = len(pcm) - len(pcm)%2
		}
		if endByte <= off {
			break
		}
		startSample := off / 2
		n := (endByte - off) / 2
		startNS := int64(startSample) * timebase.Seconds(1) / int64(opts.SampleRate)
		endNS := int64(startSample+n) * timebase.Seconds(1) / int64(opts.SampleRate)
		excluded := intersectsAny(timebase.Interval{Lo: startNS, Hi: endNS}, exclusions)

		var sum float64
		localPeak := 0
		for i := 0; i < n; i++ {
			lo := pcm[off+2*i]
			hi := pcm[off+2*i+1]
			v := int(int16(uint16(lo) | uint16(hi)<<8))
			abs := v
			if abs < 0 {
				abs = -abs
			}
			if abs > localPeak {
				localPeak = abs
			}
			fv := float64(v)
			sum += fv * fv
		}
		off = endByte
		if n == 0 {
			continue
		}
		rms := math.Sqrt(sum / float64(n))
		db := -96.0
		if rms > 0 {
			db = 20 * math.Log10(rms/32768.0)
		}
		w := Window{
			Interval: timebase.Interval{Lo: startNS, Hi: endNS},
			RMSDBFS:  round2(db),
			Excluded: excluded,
			Silent:   !excluded && rms <= thresholdRMS,
		}
		rep.Windows = append(rep.Windows, w)
		if excluded {
			rep.ExcludedCount++
		}
		if localPeak > peak {
			peak = localPeak
		}
		sumSquares += sum
		totalSamples += n

		if w.Silent {
			if !haveRun {
				runStart, runEnd, haveRun = startNS, endNS, true
			} else {
				runEnd = endNS
			}
		} else {
			closeRun()
		}
	}
	closeRun()

	if totalSamples > 0 {
		peakDB := -96.0
		if peak > 0 {
			peakDB = round2(20 * math.Log10(float64(peak)/32768.0))
		}
		rep.PeakDBFS = &peakDB
		meanRMS := math.Sqrt(sumSquares / float64(totalSamples))
		meanDB := -96.0
		if meanRMS > 0 {
			meanDB = round2(20 * math.Log10(meanRMS/32768.0))
		}
		rep.MeanRMSDBFS = &meanDB
		rep.TotalSamples = totalSamples
	}
	return rep
}

func intersectsAny(x timebase.Interval, ys []timebase.Interval) bool {
	for _, y := range ys {
		if x.Intersects(y) {
			return true
		}
	}
	return false
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
