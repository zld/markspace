// Package audio measures silence and signal quality on windowed PCM. All
// numbers are estimates derived from sample payload bytes and metadata; no
// decoding, resampling or transcoding is performed.
package audio

import (
	"math"
	"sort"

	"mediamerge/internal/model"
)

// Kind classifies one analysis window.
type Kind string

const (
	// KindActive means the window carries signal above the silence floor.
	KindActive Kind = "active"
	// KindAllZero means a fully covered window whose payload is all zeros.
	KindAllZero Kind = "all_zero"
	// KindBelowThreshold means a fully covered window with signal but RMS at
	// or below the silence threshold.
	KindBelowThreshold Kind = "below_threshold"
	// KindMissing means the window is not fully covered by samples. Missing
	// data is never reported as silence.
	KindMissing Kind = "missing"
)

// Window is one fixed-length analysis window.
type Window struct {
	Index      int            `json:"index"`
	Time       model.Interval `json:"time"`
	Kind       Kind           `json:"kind"`
	Peak       float64        `json:"peak_dbfs"`
	RMS        float64        `json:"rms_dbfs"`
	Bytes      int            `json:"bytes"`
	SampleRefs int            `json:"sample_refs"`
	CoverageNs int64          `json:"coverage_ns"`
}

// Silence reports whether the window counts as silent. Missing windows do
// not count.
func (w Window) Silence() bool {
	return w.Kind == KindAllZero || w.Kind == KindBelowThreshold
}

// Stats aggregates quality measurements over analyzed windows.
type Stats struct {
	Windows       int     `json:"windows"`
	Active        int     `json:"active"`
	Silent        int     `json:"silent"`
	AllZero       int     `json:"all_zero"`
	Missing       int     `json:"missing"`
	AnalyzedBytes int     `json:"analyzed_bytes"`
	PCMParts      int64   `json:"pcm_parts"`
	PeakDBFS      float64 `json:"peak_dbfs"`
	RMSDBFS       float64 `json:"rms_dbfs"`
	ClippedParts  int     `json:"clipped_parts"`
}

// Report is the per-track silence analysis.
type Report struct {
	TrackID        uint32           `json:"track_id"`
	WindowNs       int64            `json:"window_ns"`
	Windows        []Window         `json:"windows"`
	Intervals      []model.Interval `json:"silent_intervals"`
	Missing        []model.Interval `json:"missing_intervals"`
	Stats          Stats            `json:"stats"`
	Estimated      bool             `json:"estimated"`
	MaxBytesSample int              `json:"max_bytes_per_sample"`
}

// Options configures analysis.
type Options struct {
	// Window is the fixed window length; zero means 100 ms.
	Window model.NsDuration
	// SilenceDBFS is the RMS floor; zero value means -60 dBFS.
	SilenceDBFS float64
	// MaxBytesPerSample bounds how many payload bytes are read from one
	// sample (0 means 1 MiB). Longer payloads are truncated, not skipped.
	MaxBytesPerSample int
}

func (o Options) window() model.NsDuration {
	if o.Window > 0 {
		return o.Window
	}
	return model.NsDuration(100 * 1000 * 1000)
}

func (o Options) floor() float64 {
	if o.SilenceDBFS == 0 {
		return -60
	}
	return o.SilenceDBFS
}

func (o Options) maxBytes() int {
	if o.MaxBytesPerSample > 0 {
		return o.MaxBytesPerSample
	}
	return 1 << 20
}

// PayloadFunc returns the raw payload bytes for a sample. It must never
// return bytes outside the owning file's bounds.
type PayloadFunc func(s model.Sample) ([]byte, bool)

const minDB = -120.0

// Analyze computes silence windows for one audio track. Samples must be
// non-overlapping and sorted by start (the ingest recovery guarantees the
// latter after key sorting).
func Analyze(trackID uint32, samples []model.Sample, payload PayloadFunc, opts Options) Report {
	rep := Report{TrackID: trackID, WindowNs: int64(opts.window()), Estimated: true,
		MaxBytesSample: opts.maxBytes()}
	if len(samples) == 0 {
		rep.Stats.Missing = 1
		rep.Missing = []model.Interval{{}}
		return rep
	}
	ordered := append([]model.Sample(nil), samples...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Start != ordered[j].Start {
			return ordered[i].Start < ordered[j].Start
		}
		return ordered[i].Key() < ordered[j].Key()
	})

	win := opts.window()
	start := ordered[0].Start
	end := ordered[len(ordered)-1].End()
	floor := opts.floor()
	maxBytes := opts.maxBytes()

	var sumSquares float64
	var sumParts int64
	var peakAll float64

	for wStart := start; wStart < end; wStart += model.NsTime(win) {
		wEnd := wStart + model.NsTime(win)
		if wEnd > end {
			wEnd = end
		}
		w := Window{Index: len(rep.Windows), Time: model.Interval{Start: wStart, End: wEnd}}
		var peak float64
		var sumSq float64
		var parts int64
		for _, s := range ordered {
			lo, hi := s.Start, s.End()
			if hi <= wStart || lo >= wEnd {
				continue
			}
			overlap := int64(minTime(hi, wEnd) - maxTime(lo, wStart))
			if overlap <= 0 {
				continue
			}
			w.CoverageNs += overlap
			w.SampleRefs++
			data, ok := payload(s)
			if !ok {
				continue // unreadable payload counts as missing coverage
			}
			if len(data) > maxBytes {
				data = data[:maxBytes]
			}
			p, sq, n := pcm16Stats(data)
			w.Bytes += len(data)
			if p > peak {
				peak = p
			}
			sumSq += sq
			parts += n
		}
		fullCoverage := w.CoverageNs >= int64(wEnd-wStart)
		switch {
		case !fullCoverage:
			w.Kind = KindMissing
			w.Peak, w.RMS = minDB, minDB
			rep.Stats.Missing++
			rep.Missing = appendInterval(rep.Missing, w.Time)
		case parts == 0 && w.Bytes == 0:
			// Full coverage but no bytes at all is still missing data.
			w.Kind = KindMissing
			w.Peak, w.RMS = minDB, minDB
			rep.Stats.Missing++
			rep.Missing = appendInterval(rep.Missing, w.Time)
		default:
			rms := 0.0
			if parts > 0 {
				rms = math.Sqrt(sumSq / float64(parts))
			}
			w.Peak = toDBFS(peak)
			w.RMS = toDBFS(rms)
			switch {
			case peak == 0:
				w.Kind = KindAllZero
				rep.Stats.AllZero++
				rep.Stats.Silent++
				rep.Intervals = appendInterval(rep.Intervals, w.Time)
			case w.RMS <= floor:
				w.Kind = KindBelowThreshold
				rep.Stats.Silent++
				rep.Intervals = appendInterval(rep.Intervals, w.Time)
			default:
				w.Kind = KindActive
				rep.Stats.Active++
			}
			if peak > peakAll {
				peakAll = peak
			}
			sumSquares += sumSq
			sumParts += parts
		}
		rep.Stats.Windows++
		rep.Windows = append(rep.Windows, w)
	}
	rep.Stats.PeakDBFS = toDBFS(peakAll)
	if sumParts > 0 {
		rep.Stats.RMSDBFS = toDBFS(math.Sqrt(sumSquares / float64(sumParts)))
	} else {
		rep.Stats.RMSDBFS = minDB
	}
	rep.Stats.PCMParts = sumParts
	rep.Stats.AnalyzedBytes = 0
	for _, w := range rep.Windows {
		rep.Stats.AnalyzedBytes += w.Bytes
	}
	rep.Stats.ClippedParts = countClipped(ordered, payload, maxBytes)
	return rep
}

// SilentMs sums the silent intervals in whole milliseconds.
func (r Report) SilentMs() int64 {
	var ns int64
	for _, iv := range r.Intervals {
		ns += int64(iv.Len())
	}
	return ns / 1_000_000
}

// pcm16Stats interprets bytes as little-endian signed 16-bit PCM and
// returns abs peak, sum of squares and the number of parts.
func pcm16Stats(b []byte) (peak float64, sumSq float64, parts int64) {
	n := len(b) / 2
	for i := 0; i < n; i++ {
		v := int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8)
		f := float64(v) / 32768.0
		if f < 0 {
			f = -f
		}
		if f > peak {
			peak = f
		}
		sumSq += f * f
	}
	return peak, sumSq, int64(n)
}

func countClipped(samples []model.Sample, payload PayloadFunc, maxBytes int) int {
	clipped := 0
	for _, s := range samples {
		data, ok := payload(s)
		if !ok {
			continue
		}
		if len(data) > maxBytes {
			data = data[:maxBytes]
		}
		for i := 0; i+1 < len(data); i += 2 {
			if uint16(data[i])|uint16(data[i+1])<<8 >= 0xfffe {
				clipped++
			}
		}
	}
	return clipped
}

func toDBFS(v float64) float64 {
	if v <= 0 {
		return minDB
	}
	db := 20 * math.Log10(v)
	if db < minDB {
		return minDB
	}
	return db
}

func appendInterval(list []model.Interval, iv model.Interval) []model.Interval {
	if n := len(list); n > 0 && list[n-1].End == iv.Start {
		list[n-1].End = iv.End
		return list
	}
	return append(list, iv)
}

func minTime(a, b model.NsTime) model.NsTime {
	if a < b {
		return a
	}
	return b
}

func maxTime(a, b model.NsTime) model.NsTime {
	if a > b {
		return a
	}
	return b
}
