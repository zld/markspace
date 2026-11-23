// Package manifest turns an aligned master timeline into deterministic,
// byte-stable segment lists. Every boundary is a common decodable point; the
// realized duration and its deviation from the target are always recorded.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"

	"mediapack/internal/model"
	"mediapack/internal/timeline"
)

// Segment is one playable span. Start/End are master nanoseconds and are
// guaranteed to be common decodable points across every track.
type Segment struct {
	Index       int   `json:"index"`
	StartNS     int64 `json:"start_ns"`
	EndNS       int64 `json:"end_ns"`
	TargetNS    int64 `json:"target_ns"`
	ActualNS    int64 `json:"actual_ns"`
	DeviationNS int64 `json:"deviation_ns"`      // absolute |actual-target|
	Partial     bool  `json:"partial,omitempty"` // tail remainder shorter than target
}

// Plan is the segment list for one representation plus planning metadata.
type Plan struct {
	TargetSegmentNS int64     `json:"target_segment_ns"`
	ToleranceNS     int64     `json:"tolerance_ns"`
	Segments        []Segment `json:"segments"`
	Rejected        bool      `json:"rejected,omitempty"`
	RejectReason    string    `json:"reject_reason,omitempty"`
}

// Config controls segmentation.
type Config struct {
	TargetSegmentNS int64
	ToleranceNS     int64
}

// Build walks the common decodable points and cuts segments as close to the
// target duration as the available points allow. When no candidate boundary
// can satisfy the tolerance, the plan is marked rejected and the reason names
// the offending segment; callers must not publish a rejected plan.
func Build(mt *timeline.MasterTimeline, cfg Config, es *model.Errors) (*Plan, error) {
	if cfg.TargetSegmentNS <= 0 {
		es.Newf(model.StageManifest, model.CodeAlignTolerance, "", 0, model.ByteRange{},
			"target segment duration must be positive, got %d", cfg.TargetSegmentNS)
		return nil, es.Err()
	}
	plan := &Plan{TargetSegmentNS: cfg.TargetSegmentNS, ToleranceNS: cfg.ToleranceNS}
	points := mt.CommonPoints
	if len(points) < 2 {
		es.Newf(model.StageManifest, model.CodeAlignTolerance, "", 0, model.ByteRange{},
			"need at least two common decodable points, got %d", len(points))
		return nil, es.Err()
	}
	start := points[0]
	idx := 0
	lastPoint := points[len(points)-1]
	for {
		wantEnd := start + cfg.TargetSegmentNS
		end, ok := pickBoundary(points, start, wantEnd)
		if !ok {
			break
		}
		actual := end - start
		dev := actual - cfg.TargetSegmentNS
		if dev < 0 {
			dev = -dev
		}
		seg := Segment{
			Index: idx, StartNS: start, EndNS: end, TargetNS: cfg.TargetSegmentNS,
			ActualNS: actual, DeviationNS: dev,
		}
		isTail := end >= lastPoint
		// A shorter tail remainder is normal (stream ends mid-target): mark
		// it Partial and record the deviation, but never reject on its account.
		if isTail && actual < cfg.TargetSegmentNS {
			seg.Partial = true
		} else if cfg.ToleranceNS > 0 && dev > cfg.ToleranceNS {
			plan.Rejected = true
			plan.RejectReason = "segment " + strconv.FormatInt(int64(idx), 10) +
				" deviation " + strconv.FormatInt(dev, 10) + "ns exceeds tolerance " + strconv.FormatInt(cfg.ToleranceNS, 10) + "ns"
		}
		plan.Segments = append(plan.Segments, seg)
		idx++
		start = end
		if isTail {
			break
		}
	}
	if len(plan.Segments) == 0 {
		es.Newf(model.StageManifest, model.CodeAlignTolerance, "", 0, model.ByteRange{},
			"no segments could be cut from common points")
		return nil, es.Err()
	}
	if plan.Rejected {
		es.Newf(model.StageManifest, model.CodeAlignTolerance, "", 0, model.ByteRange{},
			"%s", plan.RejectReason)
		return plan, es.Err()
	}
	return plan, nil
}

// pickBoundary returns the common point strictly after start nearest to want,
// preferring the point >= want, then the closest available one.
func pickBoundary(points []int64, start, want int64) (int64, bool) {
	idx := sort.Search(len(points), func(i int) bool { return points[i] >= want })
	candidates := []int64{}
	if idx < len(points) && points[idx] > start {
		candidates = append(candidates, points[idx])
	}
	if idx > 0 && points[idx-1] > start {
		candidates = append(candidates, points[idx-1])
	}
	if len(candidates) == 0 {
		// Fall back to the next point after start even if far from target.
		j := sort.Search(len(points), func(i int) bool { return points[i] > start })
		if j >= len(points) {
			return 0, false
		}
		return points[j], true
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if abs64(c-want) < abs64(best-want) {
			best = c
		}
	}
	return best, true
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ContentHash is a stable digest of the plan's canonical JSON.
func (p *Plan) ContentHash() string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
