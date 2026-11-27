// Package evidence turns reconstructed samples and aligned events into
// offline, reproducible evidence: per-frame content hashes and grayscale
// pixel means, audio peak level in dBFS, and the signed alignment error of
// every observed event against its planned tick. Nothing here opens a
// display or an external service; all output is bytes and numbers.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"math"

	"regress/internal/clock"
	"regress/internal/model"
	"regress/internal/timeline"
)

// FrameStats is the offline evidence for one decoded video frame.
type FrameStats struct {
	Track   string  `json:"track"`
	Seq     uint32  `json:"seq"`
	Tick    int64   `json:"tick"`
	Sha256  string  `json:"sha256"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	Mean    float64 `json:"pixel_mean"`
	MeanAbs float64 `json:"pixel_mean_abs_deviation"`
}

// AudioStats is the offline evidence for one audio sample block.
type AudioStats struct {
	Track    string  `json:"track"`
	Seq      uint32  `json:"seq"`
	Tick     int64   `json:"tick"`
	Sha256   string  `json:"sha256"`
	Peak     int16   `json:"peak_amplitude"`
	PeakDBFS float64 `json:"peak_dbfs"`
	RMS      float64 `json:"rms_dbfs"`
}

// AlignmentError is the signed observed-minus-planned error of one event.
type AlignmentError struct {
	Key      string `json:"key"`
	Track    string `json:"track"`
	Planned  int64  `json:"planned_tick"`
	Observed int64  `json:"observed_tick"`
	Error    int64  `json:"error_tick"` // observed - planned
	Missing  bool   `json:"missing"`
}

// hashBytes returns the full lowercase hex SHA-256 of the given bytes.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Frame computes a frame's content hash and pixel statistics from its raw
// grayscale bytes. An empty frame is a caller error.
func Frame(trackID string, s model.Sample) (FrameStats, error) {
	if len(s.Frame) == 0 {
		return FrameStats{}, model.NewError(model.StageArtifact, "frame-empty",
			"", trackID, int64(s.Tick), errEmptyFrame)
	}
	var sum uint64
	for _, p := range s.Frame {
		sum += uint64(p)
	}
	mean := float64(sum) / float64(len(s.Frame))
	var devSum float64
	for _, p := range s.Frame {
		d := float64(p) - mean
		devSum += d * d
	}
	return FrameStats{
		Track: trackID, Seq: s.Seq, Tick: int64(s.Tick),
		Sha256: hashBytes(s.Frame), Width: s.Width, Height: s.Height,
		Mean:    mean,
		MeanAbs: math.Sqrt(devSum / float64(len(s.Frame))),
	}, nil
}

// Audio computes an audio block's content hash, peak amplitude and level in
// dBFS relative to full-scale int16 (32768). An empty block is a caller error.
func Audio(trackID string, s model.Sample) (AudioStats, error) {
	if len(s.Audio) == 0 {
		return AudioStats{}, model.NewError(model.StageArtifact, "audio-empty",
			"", trackID, int64(s.Tick), errEmptyAudio)
	}
	var peak int64
	var sqSum float64
	for _, v := range s.Audio {
		absV := int64(v)
		if absV < 0 {
			absV = -absV
		}
		if absV > peak {
			peak = absV
		}
		sqSum += float64(absV) * float64(absV)
	}
	peakDB := 20 * math.Log10(float64(peak)/32768.0)
	rms := math.Sqrt(sqSum / float64(len(s.Audio)))
	rmsDB := 20 * math.Log10(rms/32768.0)
	return AudioStats{
		Track: trackID, Seq: s.Seq, Tick: int64(s.Tick),
		Sha256: hashBytes(int16LE(s.Audio)),
		Peak:   int16(peak), PeakDBFS: peakDB, RMS: rmsDB,
	}, nil
}

// Alignment walks the plan-aligned timeline and reports every event's signed
// alignment error. Missing items are included with Missing=true so a loss is
// visible in the evidence rather than silently omitted.
func Alignment(tl *timeline.Timeline) []AlignmentError {
	out := make([]AlignmentError, 0, len(tl.Alignment))
	for _, a := range tl.Alignment {
		ae := AlignmentError{
			Key:      a.Plan.Key,
			Track:    a.Plan.Track,
			Planned:  int64(a.Plan.ExpectedTick),
			Observed: int64(a.ObservedTick),
			Missing:  a.Missing,
		}
		if !a.Missing {
			// a.Delay is ObservedTick - ExpectedTick, computed once during
			// alignment so the two cannot drift.
			ae.Error = int64(a.Delay)
		}
		out = append(out, ae)
	}
	return out
}

// MaxAbsAlignmentError returns the largest absolute alignment error across
// observed events, and the count of missing events alongside it.
func MaxAbsAlignmentError(errs []AlignmentError) (clock.Tick, int) {
	max, missing, _ := alignmentTotals(errs, nil)
	return max, missing
}

// alignmentTotals computes the max absolute error, missing count and, when a
// timeline is supplied, the number of conflicting alignments.
func alignmentTotals(errs []AlignmentError, tl *timeline.Timeline) (clock.Tick, int, int) {
	var max clock.Tick
	var missing, conflict int
	for _, e := range errs {
		if e.Missing {
			missing++
			continue
		}
		abs := e.Error
		if abs < 0 {
			abs = -abs
		}
		if clock.Tick(abs) > max {
			max = clock.Tick(abs)
		}
	}
	if tl != nil {
		for _, a := range tl.Alignment {
			if a.Conflict {
				conflict++
			}
		}
	}
	return max, missing, conflict
}

func int16LE(pcm []int16) []byte {
	out := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		x := uint16(v)
		out[i*2] = byte(x)
		out[i*2+1] = byte(x >> 8)
	}
	return out
}
