// Package clock estimates RTP clock drift against a monotonic sample
// time base. Drift is reported, never corrected: input timestamps stay
// untouched and out-of-threshold regions are marked as ranges.
package clock

import (
	"fmt"

	"rtx/internal/model"
)

// Sample pairs a monotonic sample-time (nanoseconds) with the unwrapped
// RTP clock reading (nanoseconds) observed for the same packet.
type Sample struct {
	Index    int   // packet index in the ordered trace
	SampleNS int64 // monotonic sample time
	RTPNS    int64 // unwrapped RTP time
}

// Result is the drift estimate over one continuous region.
type Result struct {
	PPM       float64   // estimated clock drift in parts per million
	Slope     float64   // rtp-ns per sample-ns
	Residual  []float64 // per-sample residual in nanoseconds
	MaxResid  float64   // largest absolute residual
	Anomalies []model.Anomaly
	Exceeds   []model.Range // packet-index ranges beyond the drift threshold
}

// Fit regresses RTP time onto monotonic sample time with least squares
// and returns the drift estimate. Ranges whose local drift exceeds
// maxPPM are marked in Exceeds and reported as anomalies; the input
// samples are never modified.
func Fit(samples []Sample, maxPPM float64) *Result {
	res := &Result{}
	n := len(samples)
	if n < 2 || maxPPM <= 0 {
		return res
	}
	var sx, sy float64
	for _, s := range samples {
		sx += float64(s.SampleNS)
		sy += float64(s.RTPNS)
	}
	mx, my := sx/float64(n), sy/float64(n)
	var sxy, sxx float64
	for _, s := range samples {
		dx := float64(s.SampleNS) - mx
		sxy += dx * (float64(s.RTPNS) - my)
		sxx += dx * dx
	}
	if sxx == 0 {
		return res
	}
	res.Slope = sxy / sxx
	res.PPM = (res.Slope - 1) * 1e6
	res.Residual = make([]float64, n)
	for i, s := range samples {
		r := float64(s.RTPNS) - (my + res.Slope*(float64(s.SampleNS)-mx))
		res.Residual[i] = r
		if r < 0 {
			r = -r
		}
		if r > res.MaxResid {
			res.MaxResid = r
		}
	}
	// Local drift: slope of each adjacent pair, smoothed over the
	// region. Contiguous pairs beyond maxPPM form Exceeds ranges.
	runStart := -1
	for i := 0; i+1 < n; i++ {
		dx := samples[i+1].SampleNS - samples[i].SampleNS
		if dx <= 0 {
			continue
		}
		local := (float64(samples[i+1].RTPNS-samples[i].RTPNS)/float64(dx) - 1) * 1e6
		bad := local > maxPPM || local < -maxPPM
		if bad && runStart < 0 {
			runStart = i
		}
		if !bad && runStart >= 0 {
			res.Exceeds = append(res.Exceeds, model.Range{Start: int64(samples[runStart].Index), End: int64(samples[i].Index + 1)})
			runStart = -1
		}
	}
	if runStart >= 0 {
		res.Exceeds = append(res.Exceeds, model.Range{Start: int64(samples[runStart].Index), End: int64(samples[n-1].Index + 1)})
	}
	for _, r := range res.Exceeds {
		res.Anomalies = append(res.Anomalies, model.Anomaly{
			Stage: model.StageClock, Reason: model.ReasonDrift,
			Offset: -1, RawValue: int64(res.PPM),
			Message:  fmt.Sprintf("local drift exceeds %.0f ppm over packets %s", maxPPM, r),
			Recovery: "mark range; exclude from coverage, keep raw timestamps",
		})
	}
	return res
}
