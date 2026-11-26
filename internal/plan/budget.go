// Package plan runs a fixed ladder of trial-encode candidates, measures each
// one deterministically (SSIM, peak bitrate, bounded work) and selects the
// first candidate that satisfies the quality floor, bitrate ceiling and work
// budget. Every rejection records an explicit, machine-readable reason.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"regress/internal/scan"
)

// Constraints bound candidate selection.
type Constraints struct {
	SSIMMin        float64 // minimum acceptable mean SSIM, e.g. 0.93
	MaxBitrateBPS  int64   // peak segment bitrate ceiling in bits/second
	WorkBudget     int64   // deterministic work unit ceiling
	Timescale      uint64  // source clock denominator (frames per second)
}

// Candidate is one rung of the fixed trial ladder.
type Candidate struct {
	ID           int
	Label        string
	Width        int
	Height       int
	BitrateBPS   int64 // target encode bitrate
	CRF          int   // capped rate-control quality
	WorkPerFrame int64 // deterministic work units charged per encoded frame
}

// Metric is the measured outcome of one trial encode.
type Metric struct {
	SSIM           float64
	BitratePeakBPS int64 // highest per-segment bitrate observed
	WorkUnits      int64 // deterministic, reproducible work estimate
	OutputBytes    int64
}

// Reject explains why a candidate was not selected.
type Reject struct {
	CandidateID int
	Code        string // mirrors fault.Code values
	Reason      string
}

// CandidateResult pairs a rung with its measurement and verdict.
type CandidateResult struct {
	Candidate Candidate
	Metric    Metric
	Accepted  bool
	Rejects   []Reject
}

// Ladder returns the fixed, ordered candidate gradient. Rungs are evaluated in
// order and the first feasible one wins, so the list runs from high to low
// quality; later rungs are progressively cheaper and lower bitrate.
func Ladder(width, height int) []Candidate {
	return []Candidate{
		{ID: 0, Label: "rung-0-high", Width: width, Height: height,
			BitrateBPS: 1_200_000, CRF: 18, WorkPerFrame: 400},
		{ID: 1, Label: "rung-1", Width: width, Height: height,
			BitrateBPS: 900_000, CRF: 21, WorkPerFrame: 320},
		{ID: 2, Label: "rung-2", Width: width, Height: height,
			BitrateBPS: 600_000, CRF: 24, WorkPerFrame: 250},
		{ID: 3, Label: "rung-3", Width: (width * 3) / 4, Height: (height * 3) / 4,
			BitrateBPS: 450_000, CRF: 27, WorkPerFrame: 190},
		{ID: 4, Label: "rung-4-low", Width: width / 2, Height: height / 2,
			BitrateBPS: 300_000, CRF: 30, WorkPerFrame: 140},
	}
}

// DeterministicWork derives a reproducible work estimate from the encoded
// output and the rung's per-frame charge. It never depends on wall-clock
// timing so repeated runs select the same candidate.
func DeterministicWork(c Candidate, frameCount int, outputBytes int64) int64 {
	return c.WorkPerFrame*int64(frameCount) + outputBytes/1024
}

// ConfigHash is the stable content hash of a selected configuration, used by
// the recovery journal to claim previously published products. It folds in
// every field that changes the encoded output.
func ConfigHash(codec string, c Candidate, con Constraints, keyframeCount int) string {
	h := sha256.New()
	write := func(s string) { h.Write([]byte(s)) }
	write(codec)
	write("|")
	write(c.Label)
	write("|")
	writeInt(h, int64(c.Width))
	writeInt(h, int64(c.Height))
	writeInt(h, c.BitrateBPS)
	writeInt(h, int64(c.CRF))
	writeInt(h, c.WorkPerFrame)
	write("|")
	writeFloat(h, con.SSIMMin)
	writeInt(h, con.MaxBitrateBPS)
	writeInt(h, con.WorkBudget)
	writeInt(h, int64(con.Timescale))
	writeInt(h, int64(keyframeCount))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Evaluate applies the three hard constraints to a measured candidate and
// returns the rejection list (empty when accepted).
func Evaluate(c Candidate, m Metric, con Constraints) []Reject {
	var rejects []Reject
	if m.SSIM < con.SSIMMin {
		rejects = append(rejects, Reject{c.ID, "candidate_ssim_below_minimum",
			"ssim below quality floor"})
	}
	if m.BitratePeakBPS > con.MaxBitrateBPS {
		rejects = append(rejects, Reject{c.ID, "candidate_bitrate_above_maximum",
			"peak bitrate exceeds ceiling"})
	}
	if m.WorkUnits > con.WorkBudget {
		rejects = append(rejects, Reject{c.ID, "candidate_work_over_budget",
			"deterministic work exceeds budget"})
	}
	return rejects
}

// SortedResults returns results ordered by candidate ID; useful for stable
// reports and tests.
func SortedResults(in []CandidateResult) []CandidateResult {
	out := append([]CandidateResult(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Candidate.ID < out[j].Candidate.ID })
	return out
}

// SegmentPlan describes the segment boundaries shared by every representation.
// Boundaries are derived solely from the validated keyframe table so all
// renditions cut at identical source positions.
type SegmentPlan struct {
	KeyframeOrdinals []int
	TimeDen          uint64
	FramesPerSegment []int
}

// BuildSegmentPlan turns a scan into per-segment frame counts using the
// keyframe table; the final segment absorbs all trailing frames.
func BuildSegmentPlan(res *scan.ScanResult, timeDen uint64) *SegmentPlan {
	kf := append([]int(nil), res.Keyframes...)
	sp := &SegmentPlan{TimeDen: timeDen, KeyframeOrdinals: kf}
	for i, start := range kf {
		end := len(res.AccessUnits)
		if i+1 < len(kf) {
			end = kf[i+1]
		}
		sp.FramesPerSegment = append(sp.FramesPerSegment, end-start)
	}
	return sp
}
