// Package index derives real keyframe intervals from parsed sample tables
// and records which keyframes a damaged byte range invalidates.
package index

import (
	"fmt"
	"sort"

	"mediad/internal/core"
	"mediad/internal/diagnose"
	"mediad/internal/ingest"
)

// Keyframe is one sync sample with its true position and forward gap.
type Keyframe struct {
	Number   int           `json:"number"`    // 1-based sample number
	DTS      int64         `json:"dts"`       // track ticks
	PTS      int64         `json:"pts"`       // track ticks
	GapTicks int64         `json:"gap_ticks"` // ticks since the previous keyframe
	Gap      core.Rational `json:"gap"`       // seconds
	Offset   int64         `json:"offset"`    // absolute byte offset
	Span     core.Span     `json:"span"`
	Trusted  bool          `json:"trusted"` // false when a damaged range covers it
}

// Index is the keyframe view of one track.
type Index struct {
	Asset      string        `json:"asset"`
	TrackID    uint32        `json:"track_id"`
	Timescale  uint32        `json:"timescale"`
	Keyframes  []Keyframe    `json:"keyframes"`
	FrameCount int           `json:"frame_count"`
	VFR        bool          `json:"vfr"`
	MinGap     core.Rational `json:"min_gap"`
	MaxGap     core.Rational `json:"max_gap"`
	MeanGap    core.Rational `json:"mean_gap"`
	Trusted    int           `json:"trusted_keyframes"`
	Untrusted  int           `json:"untrusted_keyframes"`
}

// Build computes keyframe intervals from real sample DTS values. It refuses to
// substitute an average frame rate: a VFR gap is whatever stts actually says.
func Build(asset string, tr ingest.MP4Track, damaged []core.Span) (*Index, diagnose.Issues) {
	idx := &Index{Asset: asset, TrackID: tr.TrackID, Timescale: tr.Timescale, FrameCount: len(tr.Samples)}
	var issues diagnose.Issues
	if tr.Timescale == 0 {
		issues.Add(diagnose.Issue{
			Category: diagnose.CatIndexOutOfRange, Stage: diagnose.StageIndex,
			Format: "mp4", Asset: asset, Span: tr.Span,
			Detail: "track has zero timescale",
		})
		return idx, issues
	}
	prevDTS := int64(-1)
	prevNum := 0
	for _, s := range tr.Samples {
		if !s.Sync {
			continue
		}
		kf := Keyframe{
			Number:  s.Number,
			DTS:     s.DTS,
			PTS:     s.PTS,
			Offset:  s.Offset,
			Span:    s.Span,
			Trusted: true,
		}
		if prevDTS >= 0 {
			kf.GapTicks = s.DTS - prevDTS
			if kf.GapTicks <= 0 {
				issues.Add(diagnose.Issue{
					Category: diagnose.CatMonotonic, Stage: diagnose.StageIndex,
					Format: "mp4", Asset: asset, Span: s.Span,
					Detail: fmt.Sprintf("keyframe %d DTS %d does not advance past keyframe %d DTS %d",
						s.Number, s.DTS, prevNum, prevDTS),
				})
			}
			kf.Gap = core.Rat(kf.GapTicks, int64(tr.Timescale))
		}
		for _, d := range damaged {
			if d.Start < kf.Span.End && d.End > kf.Span.Start {
				kf.Trusted = false
				break
			}
		}
		idx.Keyframes = append(idx.Keyframes, kf)
		prevDTS = s.DTS
		prevNum = s.Number
	}
	if len(idx.Keyframes) == 0 {
		issues.Add(diagnose.Issue{
			Category: diagnose.CatIndexOutOfRange, Stage: diagnose.StageIndex,
			Format: "mp4", Asset: asset, Span: tr.Span,
			Detail: "track declares no sync samples",
		})
		return idx, issues
	}
	gaps := make([]core.Rational, 0, len(idx.Keyframes)-1)
	minGap, maxGap := core.Zero(), core.Zero()
	sumTicks := int64(0)
	for i, kf := range idx.Keyframes {
		if kf.Trusted {
			idx.Trusted++
		} else {
			idx.Untrusted++
		}
		if i == 0 {
			continue
		}
		gaps = append(gaps, kf.Gap)
		sumTicks += kf.GapTicks
		if i == 1 || kf.Gap.Cmp(minGap) < 0 {
			minGap = kf.Gap
		}
		if i == 1 || kf.Gap.Cmp(maxGap) > 0 {
			maxGap = kf.Gap
		}
	}
	if len(gaps) > 0 {
		idx.MinGap = minGap
		idx.MaxGap = maxGap
		idx.MeanGap = core.Rat(sumTicks, int64(tr.Timescale)*int64(len(gaps)))
		idx.VFR = minGap.Cmp(maxGap) != 0
	}
	return idx, issues
}

// BuildAll builds one index per video track. Audio tracks are sampled too so
// cross-track alignment can reference their sync lattice.
func BuildAll(asset string, file *ingest.MP4File, damaged []core.Span) ([]*Index, diagnose.Issues) {
	var out []*Index
	var issues diagnose.Issues
	for _, tr := range file.Tracks {
		idx, is := Build(asset, tr, damaged)
		out = append(out, idx)
		issues = append(issues, is...)
	}
	return out, issues
}

// GapsTicks returns the ordered keyframe gaps in track ticks.
func (i *Index) GapsTicks() []int64 {
	out := make([]int64, 0, len(i.Keyframes))
	for k := 1; k < len(i.Keyframes); k++ {
		out = append(out, i.Keyframes[k].GapTicks)
	}
	return out
}

// UntrustedKeyframes lists sample numbers whose byte span is inside damage.
func (i *Index) UntrustedKeyframes() []int {
	var out []int
	for _, kf := range i.Keyframes {
		if !kf.Trusted {
			out = append(out, kf.Number)
		}
	}
	sort.Ints(out)
	return out
}

// Validate checks that every keyframe references a span inside the asset and
// that gaps are positive and consistent with the declared timescale.
func (i *Index) Validate(assetSize int64) error {
	seen := make(map[int]bool, len(i.Keyframes))
	var prevDTS int64 = -1
	for n, kf := range i.Keyframes {
		if kf.Number < 1 || kf.Number > i.FrameCount {
			return fmt.Errorf("index: keyframe %d has sample number %d outside [1,%d]", n, kf.Number, i.FrameCount)
		}
		if seen[kf.Number] {
			return fmt.Errorf("index: duplicate keyframe sample %d", kf.Number)
		}
		seen[kf.Number] = true
		if kf.Offset >= 0 && !kf.Span.Within(assetSize) {
			return fmt.Errorf("index: keyframe %d span [%d,%d) outside asset %d", kf.Number, kf.Span.Start, kf.Span.End, assetSize)
		}
		if prevDTS >= 0 && kf.DTS <= prevDTS {
			return fmt.Errorf("index: keyframe DTS not increasing at sample %d", kf.Number)
		}
		if n > 0 && kf.Gap.Num <= 0 {
			return fmt.Errorf("index: keyframe %d has non-positive gap", kf.Number)
		}
		prevDTS = kf.DTS
	}
	return nil
}
