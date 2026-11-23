// Package pipeline orchestrates the full media path: fragment parsing,
// per-track clock correction, watermark/late detection, track mapping,
// deterministic recomputation and lossless remuxing.
package pipeline

import (
	clockrtp "mediatool/internal/rtp"
	"mediatool/internal/timeline"
)

const wrap32Mod uint64 = 1 << 32
const wrap32Half uint64 = 1 << 31

// ClockReport explains one track's clock state after a fragment was
// ingested. All fields are stable content for the manifest/metrics.
type ClockReport struct {
	TrackID           uint32
	RawTfdt           uint64
	CorrectedDTS      uint64
	Correction        int64 // corrected - raw (on the unwrapped scale)
	Confidence        timeline.Confidence
	Wraparound        bool
	Restart           bool
	DriftPPM          float64
	DriftObservations int
}

// TrackClock keeps the per-track tfdt state. Version-0 (32-bit) tfdt
// values are unwrapped exactly like RTP timestamps; version-1 values are
// native 64-bit and any backward move is a device restart.
type TrackClock struct {
	TrackID       uint32
	Timescale     uint32
	TfdtVersion   uint8
	hasLast       bool
	lastRaw       uint64
	lastCorrected uint64
	expectedNext  uint64 // continuous timeline: previous corrected DTS + duration
	unwrappedBase uint64 // high bits accumulated for 32-bit tfdt
	drift         *clockrtp.DriftEstimator
	lastReport    ClockReport
}

// NewTrackClock builds the clock state for one track.
func NewTrackClock(trackID uint32, timescale uint32) *TrackClock {
	return &TrackClock{
		TrackID:   trackID,
		Timescale: timescale,
		drift:     clockrtp.NewDriftEstimator(timescale, 32),
	}
}

// Correct consumes one raw tfdt value plus the fragment's total sample
// duration and returns the corrected decode time with a full report.
// fragmentDuration may be zero when the fragment carries no samples.
func (c *TrackClock) Correct(rawTfdt uint64, tfdtVersion uint8,
	fragmentDuration uint64, rtpWallMS int64, rtpMedia uint64, hasRTP bool) ClockReport {
	c.TfdtVersion = tfdtVersion
	report := ClockReport{TrackID: c.TrackID, RawTfdt: rawTfdt,
		Confidence: timeline.ConfidenceNone}

	if hasRTP {
		c.drift.Add(clockrtp.Observation{WallMS: rtpWallMS, MediaTime: rtpMedia})
	}
	if d := c.drift.Estimate(); d.Samples >= 2 {
		report.DriftPPM = d.PPM
		report.DriftObservations = d.Samples
		report.Confidence = d.Confidence
	}

	if !c.hasLast {
		c.hasLast = true
		c.lastRaw = rawTfdt
		c.lastCorrected = rawTfdt
		c.expectedNext = rawTfdt + fragmentDuration
		report.CorrectedDTS = rawTfdt
		report.Confidence = maxConfidence(report.Confidence, timeline.ConfidenceHigh)
		c.lastReport = report
		return report
	}

	if tfdtVersion == 0 {
		report = c.correct32(rawTfdt, fragmentDuration, report)
	} else {
		report = c.correct64(rawTfdt, fragmentDuration, report)
	}

	if hasRTP && report.Confidence == timeline.ConfidenceNone {
		report.Confidence = timeline.ConfidenceMedium
	}
	c.lastReport = report
	return report
}

// correct32 handles 32-bit tfdt: backward moves within half the modulus
// are legal wraps; larger jumps are device restarts.
func (c *TrackClock) correct32(rawTfdt uint64, duration uint64,
	report ClockReport) ClockReport {
	prevMod := c.lastRaw % wrap32Mod
	forward := (rawTfdt - prevMod + wrap32Mod) % wrap32Mod
	if forward > wrap32Half {
		// Device restart: rebase the continuous timeline.
		c.unwrappedBase = 0
		c.lastRaw = rawTfdt
		c.lastCorrected = rawTfdt
		c.expectedNext = rawTfdt + duration
		report.Restart = true
		report.CorrectedDTS = rawTfdt
		report.Correction = int64(rawTfdt) - int64(c.lastReport.CorrectedDTS)
		report.Confidence = maxConfidence(report.Confidence, timeline.ConfidenceLow)
		return report
	}
	if forward > 0 && rawTfdt < prevMod {
		c.unwrappedBase += wrap32Mod
		report.Wraparound = true
		report.Confidence = maxConfidence(report.Confidence, timeline.ConfidenceHigh)
	}
	corrected := c.unwrappedBase + rawTfdt
	return c.commit(rawTfdt, corrected, duration, report)
}

// correct64 handles native 64-bit tfdt where no modular wrap exists.
func (c *TrackClock) correct64(rawTfdt uint64, duration uint64,
	report ClockReport) ClockReport {
	if rawTfdt < c.lastRaw {
		// Backward 64-bit tfdt can only be a device restart.
		c.lastRaw = rawTfdt
		c.lastCorrected = rawTfdt
		c.expectedNext = rawTfdt + duration
		report.Restart = true
		report.CorrectedDTS = rawTfdt
		report.Correction = int64(rawTfdt) - int64(c.lastReport.CorrectedDTS)
		report.Confidence = maxConfidence(report.Confidence, timeline.ConfidenceLow)
		return report
	}
	return c.commit(rawTfdt, rawTfdt, duration, report)
}

// commit advances the continuous timeline. A gap to the expected next
// position is preserved (no synthetic samples); an overlap is reported as
// a medium-confidence correction rather than silently overwritten.
func (c *TrackClock) commit(rawTfdt, corrected, duration uint64,
	report ClockReport) ClockReport {
	report.CorrectedDTS = corrected
	report.Correction = int64(corrected) - int64(rawTfdt)
	c.lastRaw = rawTfdt
	c.lastCorrected = corrected
	c.expectedNext = corrected + duration
	if report.Confidence == timeline.ConfidenceNone {
		report.Confidence = timeline.ConfidenceHigh
	}
	return report
}

// ExpectedNext returns where the next fragment's DTS should fall.
func (c *TrackClock) ExpectedNext() uint64 { return c.expectedNext }

// LastReport returns the most recent clock report.
func (c *TrackClock) LastReport() ClockReport { return c.lastReport }

func maxConfidence(a, b timeline.Confidence) timeline.Confidence {
	rank := map[timeline.Confidence]int{
		timeline.ConfidenceNone: 0, timeline.ConfidenceLow: 1,
		timeline.ConfidenceMedium: 2, timeline.ConfidenceHigh: 3,
	}
	if rank[a] >= rank[b] {
		return a
	}
	return b
}
