// Package validate checks end-to-end timeline invariants on the shared
// rational time base: audio/video synchronization, render gap propagation,
// recovery-rate and latency thresholds. Every threshold failure carries the
// affected track, frame time and the original source byte offset.
package validate

import (
	"sort"

	"mediaforensics/internal/clock"
	"mediaforensics/internal/model"
	"mediaforensics/internal/render"
	"mediaforensics/internal/repair"
	"mediaforensics/internal/rtpstats"
)

// Thresholds are the acceptance limits; zero values fall back to the defaults
// used by the command line acceptance demo.
type Thresholds struct {
	SyncErrorMs   float64
	RecoveredRate float64
	P95LatencyMs  float64
}

// DefaultThresholds matches the acceptance contract.
func DefaultThresholds() Thresholds {
	return Thresholds{SyncErrorMs: 20.0, RecoveredRate: 0.90, P95LatencyMs: 50.0}
}

// SyncFailure is one over-threshold audio/video mismatch with full provenance.
type SyncFailure struct {
	TrackID      int     `json:"track_id"`
	Frame        int     `json:"frame"`
	FrameTime    string  `json:"frame_time"`
	SyncErrorMs  float64 `json:"sync_error_ms"`
	SourceOffset int64   `json:"source_offset"`
	Stage        string  `json:"stage"`
}

// Report is the deterministic validator output.
type Report struct {
	MaxSyncErrorMs float64                `json:"max_sync_error_ms"`
	SyncFailures   []SyncFailure          `json:"sync_failures"`
	GapFrames      int                    `json:"gap_frames"`
	RepairRegions  int                    `json:"repair_regions"`
	BlockedTracks  []int                  `json:"blocked_tracks"`
	RecoveredRate  float64                `json:"recovered_rate"`
	P95LatencyMs   float64                `json:"p95_latency_ms"`
	Threshold      Thresholds             `json:"thresholds"`
	Errors         []*model.ForensicError `json:"errors"`
}

// Check runs every invariant. It never aborts on the first failure so the
// report contains the complete evidence set for the manifest.
func Check(tl *model.Timeline, fm *render.FrameManifest, rep *repair.Report,
	stats rtpstats.Stats, th Thresholds) *Report {

	out := &Report{Threshold: th, GapFrames: fm.GapCount}
	if rep != nil {
		out.RepairRegions = len(rep.Regions)
	}
	for _, tr := range tl.Tracks {
		if tr.Blocked {
			out.BlockedTracks = append(out.BlockedTracks, tr.ID)
			out.Errors = append(out.Errors, model.NewError(model.StageValidate,
				"blocked_track", tr.ID, tr.EarliestOffset, tr.BlockReason))
		}
	}
	out.checkSync(tl, fm, th)
	out.RecoveredRate = stats.RecoveredRate()
	out.P95LatencyMs = stats.P95LatencyMs
	if out.RecoveredRate < th.RecoveredRate {
		out.Errors = append(out.Errors, model.NewError(model.StageValidate,
			"recovery_rate", -1, -1, "recovered rate below threshold"))
	}
	if out.P95LatencyMs > th.P95LatencyMs {
		out.Errors = append(out.Errors, model.NewError(model.StageValidate,
			"p95_latency", -1, -1, "p95 latency above threshold"))
	}
	if fm.GapCount > 0 {
		// Propagate the first gap's source offset so frame gaps trace to bytes.
		if f := firstGap(fm); f != nil {
			out.Errors = append(out.Errors, model.NewError(model.StageValidate,
				"frame_gap", f.PrimaryTrack, gapOffset(f, tl),
				"render gap propagated into validation"))
		}
	}
	return out
}

// checkSync compares each rendered video frame time with the nearest audio
// sample time, both expressed on the same rational-seconds base.
func (r *Report) checkSync(tl *model.Timeline, fm *render.FrameManifest, th Thresholds) {
	var audio *model.Track
	for _, tr := range tl.Tracks {
		if tr.Kind == model.KindAudio && !tr.Blocked {
			audio = tr
			break
		}
	}
	if audio == nil || len(fm.Frames) == 0 {
		return
	}
	audioSamples := audio.ValidSamples()
	for _, f := range fm.Frames {
		at := parseFrameTime(f.OutputTime)
		nearest, ok := nearestSample(audioSamples, at)
		if !ok {
			continue
		}
		diff, err := at.Sub(nearest.Start)
		if err != nil {
			r.Errors = append(r.Errors, model.NewError(model.StageValidate,
				"sync_overflow", audio.ID, nearest.Offset, err.Error()))
			continue
		}
		errMs := absMS(diff)
		if errMs > r.MaxSyncErrorMs {
			r.MaxSyncErrorMs = errMs
		}
		if errMs > th.SyncErrorMs {
			r.SyncFailures = append(r.SyncFailures, SyncFailure{
				TrackID:      audio.ID,
				Frame:        f.Frame,
				FrameTime:    f.OutputTime,
				SyncErrorMs:  errMs,
				SourceOffset: nearest.Offset,
				Stage:        model.StageValidate,
			})
			r.Errors = append(r.Errors, model.NewError(model.StageValidate,
				"sync_threshold", audio.ID, nearest.Offset,
				"audio/video sync error exceeds threshold"))
		}
	}
	sort.Slice(r.SyncFailures, func(i, j int) bool {
		return r.SyncFailures[i].Frame < r.SyncFailures[j].Frame
	})
}

func nearestSample(samples []model.Sample, at clock.Rational) (model.Sample, bool) {
	if len(samples) == 0 {
		return model.Sample{}, false
	}
	best := samples[0]
	bestDiff, _ := at.Sub(best.Start)
	if bestDiff.IsNegative() {
		bestDiff = clock.New(-bestDiff.Num(), bestDiff.Den())
	}
	for _, s := range samples[1:] {
		d, err := at.Sub(s.Start)
		if err != nil {
			continue
		}
		if d.IsNegative() {
			d = clock.New(-d.Num(), d.Den())
		}
		cmp, _ := d.Cmp(bestDiff)
		if cmp < 0 {
			best, bestDiff = s, d
		}
	}
	return best, true
}

func parseFrameTime(text string) clock.Rational {
	var n, d int64
	if _, err := scanRational(text, &n, &d); err != nil {
		return clock.Zero
	}
	if d == 0 {
		return clock.Zero
	}
	return clock.New(n, d)
}

func absMS(r clock.Rational) float64 {
	v := r.Milliseconds()
	if v < 0 {
		return -v
	}
	return v
}

func firstGap(fm *render.FrameManifest) *render.Provenance {
	for i := range fm.Frames {
		if fm.Frames[i].Gap {
			return &fm.Frames[i]
		}
	}
	return nil
}

func gapOffset(f *render.Provenance, tl *model.Timeline) int64 {
	if tr := tl.Track(f.PrimaryTrack); tr != nil && tr.EarliestOffset >= 0 {
		return tr.EarliestOffset
	}
	return f.PrimaryOffset
}
