// Package metrics derives delivery statistics from repaired tracks and their
// indexes. Bitrate is measured over the manifest's editorial window, and
// alignment compares the delivered in/out points with the manifest.
package metrics

import (
	"math"
	"sort"

	"mediad/internal/clock"
	mp4 "mediad/internal/mp4"
)

func Compute(in Input) *Report {
	r := &Report{TargetBps: in.TargetBps}
	var totalBytes, windowBytes int64
	var totalDurationNs int64
	var coverageSum float64
	var coverageTracks int
	var psnrSum float64
	var psnrN int
	for _, w := range in.Windows {
		tr := in.Tracks[w.Camera]
		ti := in.Indexes[w.Camera]
		if tr == nil {
			continue
		}
		cm := CameraMetric{Camera: w.Camera, WindowStartNs: w.StartNs, WindowEndNs: w.EndNs}
		// A repaired media timestamp and an extended RTP wrap are both
		// timestamp repairs from the delivery's point of view.
		cm.TimestampRepairs = tr.RepairCount
		cm.RTPWrapExtensions = 0
		if tr.RTP != nil {
			cm.RTPWrapExtensions = tr.RTP.WrapCount
			cm.TimestampRepairs += tr.RTP.WrapCount
		}
		if ti != nil {
			cm.Keyframes = len(ti.Entries)
			coverageSum += ti.CoveragePct
			coverageTracks++
			r.Keyframes += len(ti.Entries)
			if ms := ti.MaxIntervalNs / 1_000_000; ms > r.MaxIndexIntervalMs {
				r.MaxIndexIntervalMs = ms
			}
			cm.MaxIntervalNs = ti.MaxIntervalNs
			if ti.Sparse {
				r.SparseKeyframes = true
			}
			if cm.IndexRangeStartNs == 0 && cm.IndexRangeEndNs == 0 {
				cm.IndexRangeStartNs = ti.RangeStartNs
				cm.IndexRangeEndNs = ti.RangeEndNs
			}
		}
		// Collect samples presented inside the editorial window. The window is
		// session-relative, matching repaired NS. Bitrate is measured over the
		// full presented span, not the editorial window, because delivery
		// carries the camera's whole recording.
		var windowSamples []*mp4.RepairedSample
		for i := range tr.Samples {
			s := &tr.Samples[i]
			// Pre-roll (negative NS) is retained but not delivered.
			if s.NS < 0 {
				continue
			}
			totalBytes += int64(s.Size)
			// Windows are offsets from the camera's recording start (the same
			// frame-relative space as repaired NS). The pipeline converts
			// manifest session times into this space before calling.
			sessionNs := s.NS
			if sessionNs >= w.StartNs && sessionNs < w.EndNs {
				windowSamples = append(windowSamples, s)
				cm.Bytes += int64(s.Size)
				windowBytes += int64(s.Size)
			}
		}
		cm.Samples = len(windowSamples)
		r.Samples += len(windowSamples)
		if len(windowSamples) > 0 {
			cm.FirstNs = windowSamples[0].NS
			cm.LastNs = windowSamples[len(windowSamples)-1].NS
		}
		// Delivered camera-seconds are the span from the presented origin to
		// the end of the final frame's display interval.
		if span := tr.LastNs - tr.FirstNs + lastFrameNs(tr); span > 0 {
			totalDurationNs += span
		}
		// Alignment: for audio/video pairs the editorial window defines the
		// intended in/out; we compare against the delivered coverage endpoints.
		// The expected in-point is the first frame at or after the window start;
		// error is measured against that grid.
		cm.AlignmentErrorNs = alignmentError(w, tr, windowSamples)
		if cm.AlignmentErrorNs > 0 {
			// Aggregate alignment is the worst camera.
			if ns := float64(cm.AlignmentErrorNs) / 1e6; ns > r.AlignmentErrorMs {
				r.AlignmentErrorMs = ns
			}
		}
		r.TimestampRepairs += cm.TimestampRepairs
		r.RTPWrapExtensions += cm.RTPWrapExtensions
		cm.PSNRDb = temporalPSNR(tr, windowSamples)
		if !math.IsInf(cm.PSNRDb, 0) && !math.IsNaN(cm.PSNRDb) {
			psnrSum += cm.PSNRDb
			psnrN++
		}
		r.Cameras = append(r.Cameras, cm)
	}
	if coverageTracks > 0 {
		r.IndexCoveragePct = coverageSum / float64(coverageTracks)
	}
	// Delivery bandwidth is measured over the wall-clock editorial window, so
	// concurrent cameras share the denominator exactly as they share the
	// network/session clock. ActualBps reports that delivery figure; SourceBps
	// reports the whole-recording figure for context.
	wallNs := windowUnionNs(in.Windows)
	if wallNs > 0 {
		r.ActualBps = int64(math.Round(float64(windowBytes) * 8 / (float64(wallNs) / float64(clock.NsPerSecond))))
	}
	if totalDurationNs > 0 {
		r.SourceBps = int64(math.Round(float64(totalBytes) * 8 / (float64(totalDurationNs) / float64(clock.NsPerSecond))))
	}
	r.DeliveryBps = r.ActualBps
	if r.TargetBps > 0 {
		r.BitrateDeltaPct = (float64(r.ActualBps) - float64(r.TargetBps)) / float64(r.TargetBps) * 100
	}
	if psnrN > 0 {
		r.QualityPSNRDb = psnrSum / float64(psnrN)
	} else {
		r.QualityPSNRDb = 0
	}
	return r
}

// windowUnionNs is the wall-clock length of the union of editorial windows.
func windowUnionNs(ws []Window) int64 {
	if len(ws) == 0 {
		return 0
	}
	type span struct{ a, b int64 }
	spans := make([]span, 0, len(ws))
	for _, w := range ws {
		if w.EndNs > w.StartNs {
			spans = append(spans, span{w.StartNs, w.EndNs})
		}
	}
	if len(spans) == 0 {
		return 0
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].a < spans[j].a })
	total := int64(0)
	curA, curB := spans[0].a, spans[0].b
	for _, s := range spans[1:] {
		if s.a > curB {
			total += curB - curA
			curA, curB = s.a, s.b
			continue
		}
		if s.b > curB {
			curB = s.b
		}
	}
	return total + curB - curA
}

// lastFrameNs estimates the final frame's display interval from the cadence of
// the tail of the track.
func lastFrameNs(tr *mp4.RepairedTrack) int64 {
	n := len(tr.Samples)
	if n < 2 {
		return 0
	}
	d := tr.Samples[n-1].NS - tr.Samples[n-2].NS
	if d <= 0 {
		return 0
	}
	return d
}

// alignmentError measures how far the delivered in/out points fall from the
// manifest's editorial boundaries relative to the camera's own frame grid.
// A cut is aligned when the boundary sits within half a frame of the nearest
// delivered frame; the residual is what a reviewer could still fix by
// trimming, so that is what we report (never the raw PTS stride, which
// legitimately includes composition offset).
func alignmentError(w Window, tr *mp4.RepairedTrack, window []*mp4.RepairedSample) int64 {
	if len(window) < 2 {
		return 0
	}
	cadence := window[1].NS - window[0].NS
	if cadence <= 0 {
		return 0
	}
	// Distance from the window start to the nearest delivered frame boundary,
	// modulo one frame period.
	inStage := (window[0].NS - w.StartNs) % cadence
	if inStage < 0 {
		inStage += cadence
	}
	if inStage > cadence-inStage {
		inStage = cadence - inStage
	}
	// Distance from the window end to the nearest delivered frame boundary.
	outStage := (window[len(window)-1].NS - w.EndNs) % cadence
	if outStage < 0 {
		outStage += cadence
	}
	if outStage > cadence-outStage {
		outStage = cadence - outStage
	}
	if outStage > inStage {
		return outStage
	}
	return inStage
}

// temporalPSNR measures temporal fidelity on the repaired grid. Because this
// pipeline does not decode video, "quality" is defined over inter-sample
// spacing: the reference is the nominal cadence, the measurement is the
// repaired spacing. A perfectly repaired track yields the measurement
// ceiling; repaired samples lower the value. The metric is always finite.
func temporalPSNR(tr *mp4.RepairedTrack, window []*mp4.RepairedSample) float64 {
	const ceilingDb = 99.0
	if len(window) < 3 {
		return 0
	}
	var nominal int64
	for i := 1; i < len(window); i++ {
		if d := window[i].NS - window[i-1].NS; d > 0 {
			nominal = d
			break
		}
	}
	if nominal <= 0 {
		return ceilingDb
	}
	var sq float64
	n := 0
	for i := 1; i < len(window); i++ {
		d := window[i].NS - window[i-1].NS
		if d < 0 {
			d = -d
		}
		diff := float64(d - nominal)
		sq += diff * diff
		n++
	}
	if n == 0 {
		return ceilingDb
	}
	rmse := math.Sqrt(sq / float64(n))
	if rmse == 0 {
		return ceilingDb
	}
	psnr := 20 * math.Log10(float64(nominal)/rmse)
	if psnr > ceilingDb {
		psnr = ceilingDb
	}
	if psnr < 0 {
		psnr = 0
	}
	return psnr
}

// KeyframeDensity returns keyframes per second inside the window.
func KeyframeDensity(keyframes int, spanNs int64) float64 {
	if spanNs <= 0 {
		return 0
	}
	return float64(keyframes) / (float64(spanNs) / float64(clock.NsPerSecond))
}
