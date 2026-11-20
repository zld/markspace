package validate

import (
	"encoding/json"
	"fmt"
	"os"

	"hlscheck/internal/fsutil"
	"hlscheck/internal/hls"
	"hlscheck/internal/model"
)

// LoadIndex reads and decodes index.json from a validated location.
func LoadIndex(root, indexPath string) (*hls.Index, error) {
	safe, err := fsutil.SafeJoinRel(root, relOrName(root, indexPath))
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(safe)
	if err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}
	var idx hls.Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}
	return &idx, nil
}

// relOrName accepts either an absolute path inside root or a plain basename,
// returning the relative form SafeJoin expects.
func relOrName(root, p string) string {
	if p == "" {
		return "index.json"
	}
	// Strip the root prefix when present so SafeJoin re-validates the
	// containment itself; a bare name passes through unchanged.
	if len(p) >= len(root) && p[:len(root)] == root {
		rest := p[len(root):]
		for len(rest) > 0 && (rest[0] == '/' || rest[0] == os.PathSeparator) {
			rest = rest[1:]
		}
		return rest
	}
	return p
}

// Run executes every check and aggregates the results. It never short
// circuits on media inconsistencies; only unreadable indexes abort early.
func Run(root string, idx *hls.Index) *Report {
	rep := &Report{OK: true}
	for i := range idx.Renditions {
		ir := &idx.Renditions[i]
		res := model.RenditionResult{Bps: ir.Rendition.Bps}
		res.SegmentCount = len(ir.Segments)
		var total int64
		for _, s := range ir.Segments {
			total += s.DurationMS()
		}
		res.DurationMS = total
		findings := checkRendition(root, ir, idx)
		res.Failures = findings
		res.FailureCount = len(findings)
		res.DeviationMS = maxDeviation(ir.Segments, idx.TargetSeconds)
		rep.Findings = append(rep.Findings, findings...)
		rep.Renditions = append(rep.Renditions, res)
	}
	rep.Findings = append(rep.Findings, checkCrossRendition(idx)...)
	if len(rep.Findings) > 0 {
		rep.OK = false
		for i := range rep.Renditions {
			rep.Renditions[i].FailureCount = countFor(rep.Findings, rep.Renditions[i].Bps)
		}
	}
	return rep
}

// maxDeviation reports the largest absolute difference between an interior
// segment's duration and the nominal target, in milliseconds. The final
// segment is allowed to be short and therefore excluded.
func maxDeviation(segs []model.Segment, targetSec int) int64 {
	targetMS := int64(targetSec) * 1000
	var worst int64
	for i, s := range segs {
		if i == len(segs)-1 {
			continue
		}
		d := s.DurationMS() - targetMS
		if d < 0 {
			d = -d
		}
		if d > worst {
			worst = d
		}
	}
	return worst
}

func countFor(findings []model.Finding, bps int) int {
	n := 0
	for _, f := range findings {
		if f.Bps == bps {
			n++
		}
	}
	return n
}
