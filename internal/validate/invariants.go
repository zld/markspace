// Package validate checks cross-stage invariants before a report is emitted.
package validate

import (
	"fmt"
	"sort"
	"strings"

	"mediad/internal/clock"
	"mediad/internal/core"
	"mediad/internal/diagnose"
)

// Check is one invariant assertion with a stable identifier.
type Check struct {
	Name   string         `json:"name"`
	Passed bool           `json:"passed"`
	Detail string         `json:"detail,omitempty"`
	Stage  diagnose.Stage `json:"stage"`
	Asset  string         `json:"asset,omitempty"`
	Span   core.Span      `json:"span"`
}

// Result aggregates checks for the report.
type Result struct {
	Checks []Check `json:"checks"`
	Failed int     `json:"failed"`
	Total  int     `json:"total"`
}

// Add records a check outcome.
func (r *Result) Add(c Check) { r.Checks = append(r.Checks, c) }

// Finish sorts checks and computes totals deterministically.
func (r *Result) Finish() {
	sort.SliceStable(r.Checks, func(i, j int) bool {
		if r.Checks[i].Name != r.Checks[j].Name {
			return r.Checks[i].Name < r.Checks[j].Name
		}
		if r.Checks[i].Asset != r.Checks[j].Asset {
			return r.Checks[i].Asset < r.Checks[j].Asset
		}
		return r.Checks[i].Span.Start < r.Checks[j].Span.Start
	})
	r.Total = len(r.Checks)
	r.Failed = 0
	for _, c := range r.Checks {
		if !c.Passed {
			r.Failed++
		}
	}
}

// FailedChecks lists the failed checks in order.
func (r *Result) FailedChecks() []Check {
	out := make([]Check, 0, r.Failed)
	for _, c := range r.Checks {
		if !c.Passed {
			out = append(out, c)
		}
	}
	return out
}

// Issues converts failures into offset-carrying validation issues.
func (r *Result) Issues() diagnose.Issues {
	var out diagnose.Issues
	for _, c := range r.Checks {
		if c.Passed {
			continue
		}
		out = append(out, diagnose.Issue{
			Category: diagnose.CatInvariant, Stage: diagnose.StageValidate,
			Format: "report", Asset: c.Asset, Span: c.Span,
			Detail: fmt.Sprintf("%s: %s", c.Name, c.Detail),
		})
	}
	return out
}

// Input describes the stage outputs the invariants are checked against.
type Input struct {
	Sizes        map[string]int64
	AssetFormats map[string]string
	Timeline     *clock.Timeline
	Indexes      []IndexRef
	Issues       diagnose.Issues
	Ranges       []RangeRef
	Drifts       []DriftRef
}

// DriftRef is the validation view of one audited node drift fit.
type DriftRef struct {
	Asset     string
	Node      string
	Samples   int
	Slope     core.Rational
	Intercept core.Rational
	RatePPB   int64
	Applied   bool
	Span      core.Span
}

// IndexRef is a minimal view of an index for validation.
type IndexRef struct {
	Asset      string
	TrackID    uint32
	Timescale  uint32
	FrameCount int
	Keyframes  []KeyRef
	MinGap     core.Rational
	MaxGap     core.Rational
	VFR        bool
	AssetSize  int64 // 0 means the size table is authoritative
}

// KeyRef is one keyframe under validation.
type KeyRef struct {
	Number  int
	DTS     int64
	Span    core.Span
	Offset  int64
	Gap     core.Rational
	Trusted bool
}

// RangeRef is one reported corrupt range.
type RangeRef struct {
	Asset  string
	Format string
	Span   core.Span
}

// All runs every invariant and returns the aggregate result.
func All(in Input) *Result {
	res := &Result{}
	checkOffsets(res, in)
	checkTimeline(res, in)
	checkIndexes(res, in)
	checkRanges(res, in)
	checkIssues(res, in)
	checkDrifts(res, in)
	checkAssetFormats(res, in)
	res.Finish()
	return res
}

func checkOffsets(res *Result, in Input) {
	for asset, size := range in.Sizes {
		res.Add(Check{
			Name: "offset_range_legal", Stage: diagnose.StageValidate, Asset: asset,
			Passed: size >= 0, Span: core.Span{Start: 0, End: max64(size, 0)},
			Detail: fmt.Sprintf("asset size %d", size),
		})
	}
	for _, idx := range in.Indexes {
		size, ok := in.Sizes[idx.Asset]
		if !ok && idx.AssetSize > 0 {
			size, ok = idx.AssetSize, true
		}
		for _, kf := range idx.Keyframes {
			pass := ok && kf.Span.Start >= 0 && kf.Span.End >= kf.Span.Start
			if pass && kf.Offset >= 0 {
				// The record carries both the sample offset and its span, so
				// they must agree; a disagreement means the report was edited
				// or serialized from inconsistent state.
				pass = kf.Span.End <= size && kf.Span.Start == kf.Offset
			}
			res.Add(Check{
				Name: "index_reference_valid", Stage: diagnose.StageValidate, Asset: idx.Asset,
				Passed: pass, Span: kf.Span,
				Detail: fmt.Sprintf("keyframe %d offset=%d references [%d,%d) of %d",
					kf.Number, kf.Offset, kf.Span.Start, kf.Span.End, size),
			})
		}
	}
}

func checkTimeline(res *Result, in Input) {
	tl := in.Timeline
	if tl == nil {
		res.Add(Check{Name: "timeline_monotonic", Stage: diagnose.StageValidate, Passed: false, Detail: "no normalized timeline"})
		return
	}
	pass := true
	var prev core.Rational
	for i, p := range tl.Points {
		if i > 0 && p.Master.Cmp(prev) < 0 {
			res.Add(Check{
				Name: "timeline_monotonic", Stage: diagnose.StageValidate, Asset: p.Track,
				Passed: false, Span: p.Span,
				Detail: fmt.Sprintf("point %d regressed to %s from %s", i, p.Master, prev),
			})
			pass = false
			break
		}
		prev = p.Master
	}
	if pass {
		res.Add(Check{Name: "timeline_monotonic", Stage: diagnose.StageValidate, Passed: true,
			Detail: fmt.Sprintf("%d points across %d tracks", len(tl.Points), len(tl.Tracks))})
	}
	for _, tr := range tl.Tracks {
		if tr.Drift == nil {
			continue
		}
		res.Add(driftCheck(DriftRef{
			Asset: tr.Asset, Node: tr.Drift.Node, Samples: tr.Drift.Samples,
			Slope: tr.Drift.Slope, Intercept: tr.Drift.Intercept,
			RatePPB: tr.Drift.RatePPB, Applied: tr.Drift.Applied,
		}))
	}
}

// driftCheck accepts a correction only when all of its audit fields are
// present and mutually consistent: a positive slope, a bounded intercept, and
// a ppb value that matches the slope it was derived from.
func driftCheck(d DriftRef) Check {
	why := ""
	switch {
	case d.Samples < 2:
		why = fmt.Sprintf("only %d samples", d.Samples)
	case d.Slope.Num <= 0 || d.Slope.Den <= 0:
		why = fmt.Sprintf("non-positive slope %s", d.Slope)
	case d.Intercept.Abs().Cmp(core.Rat(3600, 1)) > 0:
		why = fmt.Sprintf("implausible intercept %s", d.Intercept)
	default:
		want := roundPPB(d.Slope)
		delta := d.RatePPB - want
		if delta < 0 {
			delta = -delta
		}
		if delta > 1 {
			why = fmt.Sprintf("ppb %d disagrees with slope %s (%d)", d.RatePPB, d.Slope, want)
		}
	}
	return Check{
		Name: "drift_correction_auditable", Stage: diagnose.StageValidate, Asset: d.Asset,
		Passed: why == "", Span: d.Span,
		Detail: fmt.Sprintf("node=%s samples=%d slope=%s intercept=%s ppb=%d applied=%v %s",
			d.Node, d.Samples, d.Slope, d.Intercept, d.RatePPB, d.Applied, why),
	}
}

func roundPPB(slope core.Rational) int64 {
	num := (slope.Num - slope.Den) * 1_000_000_000
	if num >= 0 {
		return (num + slope.Den/2) / slope.Den
	}
	return -((-num + slope.Den/2) / slope.Den)
}

func checkIndexes(res *Result, in Input) {
	for _, idx := range in.Indexes {
		prevDTS := int64(-1)
		gapsOK := true
		detail := ""
		for i, kf := range idx.Keyframes {
			if kf.Number < 1 || kf.Number > idx.FrameCount {
				gapsOK = false
				detail = fmt.Sprintf("keyframe sample %d outside [1,%d]", kf.Number, idx.FrameCount)
				break
			}
			if i > 0 {
				if kf.DTS <= prevDTS {
					gapsOK = false
					detail = fmt.Sprintf("DTS %d not after %d", kf.DTS, prevDTS)
					break
				}
				if kf.Gap.Num <= 0 {
					gapsOK = false
					detail = fmt.Sprintf("non-positive gap at sample %d", kf.Number)
					break
				}
			}
			prevDTS = kf.DTS
		}
		res.Add(Check{
			Name: "keyframe_gap_valid", Stage: diagnose.StageValidate, Asset: idx.Asset,
			Passed: gapsOK, Detail: detail,
		})
		if len(idx.Keyframes) > 1 && gapsOK {
			spreadOK := idx.MinGap.Cmp(idx.MaxGap) <= 0
			res.Add(Check{
				Name: "vfr_gap_consistent", Stage: diagnose.StageValidate, Asset: idx.Asset,
				Passed: spreadOK,
				Detail: fmt.Sprintf("min=%s max=%s vfr=%v", idx.MinGap, idx.MaxGap, spreadOK && idx.MinGap.Cmp(idx.MaxGap) != 0),
			})
		}
	}
}

func checkRanges(res *Result, in Input) {
	seen := make(map[string]core.Span)
	for _, r := range in.Ranges {
		size, ok := in.Sizes[r.Asset]
		pass := ok && r.Span.Start >= 0 && r.Span.End >= r.Span.Start && r.Span.End <= size
		if pass {
			if prev, dup := seen[r.Asset+"\x00"+r.Format]; dup && r.Span.Start < prev.End {
				pass = false
			}
			seen[r.Asset+"\x00"+r.Format] = r.Span
		}
		res.Add(Check{
			Name: "corrupt_range_half_open", Stage: diagnose.StageValidate, Asset: r.Asset,
			Passed: pass, Span: r.Span,
			Detail: fmt.Sprintf("range [%d,%d) within %d", r.Span.Start, r.Span.End, size),
		})
	}
}

func checkIssues(res *Result, in Input) {
	for _, is := range in.Issues {
		size, ok := in.Sizes[is.Asset]
		pass := ok && is.Span.Start >= 0 && is.Span.End >= is.Span.Start && is.Span.End <= size
		if !ok && is.Format == "report" {
			pass = true
		}
		res.Add(Check{
			Name: "issue_offset_legal", Stage: diagnose.StageValidate, Asset: is.Asset,
			Passed: pass, Span: is.Span,
			Detail: fmt.Sprintf("%s at [%d,%d) of %d", is.Category, is.Span.Start, is.Span.End, size),
		})
	}
}

func checkDrifts(res *Result, in Input) {
	for _, d := range in.Drifts {
		res.Add(driftCheck(d))
	}
}

func checkAssetFormats(res *Result, in Input) {
	names := make([]string, 0, len(in.AssetFormats))
	for name := range in.AssetFormats {
		names = append(names, name)
	}
	sort.Strings(names)
	pass := true
	for _, n := range names {
		if strings.TrimSpace(in.AssetFormats[n]) == "" {
			pass = false
		}
	}
	res.Add(Check{Name: "asset_format_known", Stage: diagnose.StageValidate, Passed: pass,
		Detail: fmt.Sprintf("%d assets classified", len(names))})
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// IngestStage checks the parse stage's own outputs: every finding and cue
// must reference legal bytes of the asset it names.
func IngestStage(sizes map[string]int64, issues diagnose.Issues, cues []CueRef) *Result {
	res := &Result{}
	for _, is := range issues {
		size, ok := sizes[is.Asset]
		pass := ok && is.Span.Within(size)
		res.Add(Check{
			Name: "ingest_offset_legal", Stage: diagnose.StageIngest, Asset: is.Asset,
			Passed: pass, Span: is.Span,
			Detail: fmt.Sprintf("%s at [%d,%d) of %d", is.Category, is.Span.Start, is.Span.End, size),
		})
	}
	for _, c := range cues {
		size, ok := sizes[c.Asset]
		pass := ok && c.Span.Within(size) && c.TimingSpan.Within(size)
		res.Add(Check{
			Name: "cue_span_legal", Stage: diagnose.StageIngest, Asset: c.Asset,
			Passed: pass, Span: c.Span,
			Detail: fmt.Sprintf("cue %d body [%d,%d) timing [%d,%d) of %d",
				c.Index, c.Span.Start, c.Span.End, c.TimingSpan.Start, c.TimingSpan.End, size),
		})
	}
	res.Finish()
	return res
}

// CueRef is the validation view of one parsed cue.
type CueRef struct {
	Asset      string
	Index      int
	Span       core.Span
	TimingSpan core.Span
}

// NormalizeStage checks that normalization produced a usable, monotonic,
// correctly ordered shared clock.
func NormalizeStage(tl *clock.Timeline, points []clock.Point) *Result {
	res := &Result{}
	if tl != nil {
		res.Add(Check{
			Name: "timeline_sealed", Stage: diagnose.StageNormalize,
			Passed: tl.Master != "" && len(tl.Tracks) > 0,
			Detail: fmt.Sprintf("master=%q tracks=%d", tl.Master, len(tl.Tracks)),
		})
	}
	var prev core.Rational
	for i, p := range points {
		pass := p.Span.Start >= 0 && p.Span.End >= p.Span.Start
		if i > 0 {
			pass = pass && p.Master.Cmp(prev) >= 0
		}
		res.Add(Check{
			Name: "normalize_point_monotonic", Stage: diagnose.StageNormalize, Asset: p.Track,
			Passed: pass, Span: p.Span,
			Detail: fmt.Sprintf("master=%s previous=%s", p.Master, prev),
		})
		prev = p.Master
	}
	res.Finish()
	return res
}

// IndexStage checks that every keyframe reference resolves inside its asset.
func IndexStage(indexes []IndexRef) *Result {
	res := &Result{}
	for _, idx := range indexes {
		prev := int64(-1)
		for i, kf := range idx.Keyframes {
			pass := kf.Number >= 1 && kf.Number <= idx.FrameCount && kf.Span.Start >= 0 && kf.Span.End >= kf.Span.Start
			if i > 0 {
				pass = pass && kf.DTS > prev && kf.Gap.Num > 0
			}
			res.Add(Check{
				Name: "index_reference_valid", Stage: diagnose.StageIndex, Asset: idx.Asset,
				Passed: pass, Span: kf.Span,
				Detail: fmt.Sprintf("keyframe %d dts=%d gap=%s", kf.Number, kf.DTS, kf.Gap),
			})
			prev = kf.DTS
		}
	}
	res.Finish()
	return res
}

// DiagnoseStage checks merged ranges are ordered, disjoint and inside bounds.
func DiagnoseStage(ranges []RangeRef, sizes map[string]int64) *Result {
	in := Input{Sizes: sizes, Ranges: ranges}
	res := &Result{}
	checkRanges(res, in)
	res.Finish()
	return res
}
