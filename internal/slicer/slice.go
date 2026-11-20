package slicer

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hlscheck/internal/fsutil"
	"hlscheck/internal/model"
)

// Result bundles the artifacts produced for one rendition.
type Result struct {
	Rendition model.Rendition
	Plan      *Plan
	Segments  []model.Segment
}

// Options controls one slice run.
type Options struct {
	// Target is the nominal segment duration; actual cuts land on IDR frames.
	Target time.Duration
	// AlignTolerance is the maximum cross-rendition boundary difference.
	AlignTolerance int64
	// SegmentPrefix names output files as <prefix><seq>.ts.
	SegmentPrefix string
}

// Slice runs analysis then writing for one rendition and writes segments into
// root/<rendition.Dir>. All filesystem access stays below root.
func Slice(root string, r model.Rendition, opts Options) (*Result, error) {
	dir, err := fsutil.SafeJoin(root, r.Dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Open(r.Source)
	if err != nil {
		return nil, err
	}
	plan, err := Analyze(f, opts.Target)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("analyze %s: %w", filepath.Base(r.Source), err)
	}
	writer, err := NewSegmentWriter(r.Source, dir, plan, opts.SegmentPrefix)
	if err != nil {
		return nil, err
	}
	defer writer.Close()
	segments, err := writer.Write()
	if err != nil {
		return nil, fmt.Errorf("write segments for %d bps: %w", r.Bps, err)
	}
	return &Result{Rendition: r, Plan: plan, Segments: segments}, nil
}

// CheckAlignment compares boundary PTS across renditions and returns findings
// for every boundary pair differing by more than tolerance ticks. All
// mismatches are reported; the caller decides whether they are fatal.
func CheckAlignment(results []*Result, tolerance int64) []model.Finding {
	var findings []model.Finding
	if len(results) < 2 {
		return findings
	}
	ref := results[0]
	for _, other := range results[1:] {
		n := len(ref.Plan.Cuts)
		if len(other.Plan.Cuts) < n {
			n = len(other.Plan.Cuts)
		}
		for i := 0; i < n; i++ {
			delta := other.Plan.Cuts[i].StartPTS - ref.Plan.Cuts[i].StartPTS
			if delta < 0 {
				delta = -delta
			}
			if delta > tolerance {
				findings = append(findings, model.Finding{
					Code: "BOUNDARY_MISALIGNED",
					Bps:  other.Rendition.Bps,
					Seq:  i,
					Detail: fmt.Sprintf(
						"boundary %d differs from %d bps by %d ticks (limit %d)",
						i, ref.Rendition.Bps, delta, tolerance),
				})
			}
		}
		if len(other.Plan.Cuts) != len(ref.Plan.Cuts) {
			findings = append(findings, model.Finding{
				Code:   "SEGMENT_COUNT_MISMATCH",
				Bps:    other.Rendition.Bps,
				Detail: fmt.Sprintf("%d segments vs %d on %d bps", len(other.Plan.Cuts), len(ref.Plan.Cuts), ref.Rendition.Bps),
			})
		}
	}
	return findings
}
