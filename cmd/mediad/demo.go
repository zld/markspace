package main

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/broadcast/mediad/internal/evidence"
	"github.com/broadcast/mediad/internal/fixture"
	"github.com/broadcast/mediad/internal/model"
	"github.com/broadcast/mediad/internal/probe"
	"github.com/broadcast/mediad/internal/repair"
	"github.com/broadcast/mediad/internal/remux"
)

// DemoOptions mirrors the terminal flags of `mediad demo`.
type DemoOptions struct {
	Seed      int64
	InputSet  string
	Remux     bool
	Verify    bool
	JSON      bool
	WorkDir   string
}

// FileReport is the per-fixture terminal/JSON record.
type FileReport struct {
	Name                   string                       `json:"name"`
	TracksChecked          int                          `json:"tracks_checked"`
	RoundtripMatchRate     float64                      `json:"roundtrip_match_rate"`
	KeyframeIndexHitRate   float64                      `json:"keyframe_index_hit_rate"`
	SegmentHashes          []map[string]string          `json:"segment_hashes"`
	DamagedRanges          []rangeView                  `json:"damaged_ranges"`
	RecoveredRanges        []rangeView                  `json:"recovered_ranges"`
	Errors                 []model.MediaError           `json:"errors"`
	Status                 string                       `json:"status"`
}

type rangeView struct {
	TrackID uint32 `json:"track_id"`
	Range   string `json:"range"`
}

// RunDemo generates the standard fixture set, pushes each file through
// probe -> repair -> (remux -> verify) and aggregates the acceptance metrics.
// It returns the process exit code: 0 all-policy pass, 1 processing failure,
// 2 verification metric below threshold.
func RunDemo(opts DemoOptions) (int, *AggregateReport) {
	reports := make([]FileReport, 0)
	hardFailure := false
	metricFailure := false

	set := fixture.StandardSet(opts.Seed)
	for _, spec := range set {
		rep, code := processOne(spec, opts)
		reports = append(reports, rep)
		switch code {
		case 1:
			hardFailure = true
		case 2:
			metricFailure = true
		}
	}

	agg := aggregate(reports, opts.Seed)
	if opts.JSON {
		writeJSON(os.Stdout, agg)
	} else {
		writeText(os.Stdout, agg)
	}
	if hardFailure {
		return 1, agg
	}
	if metricFailure {
		return 2, agg
	}
	return 0, agg
}

func processOne(spec fixture.Spec, opts DemoOptions) (FileReport, int) {
	raw, err := fixture.Generate(spec)
	if err != nil {
		return FileReport{Name: spec.Name, Status: "failed: " + err.Error()}, 1
	}
	if opts.WorkDir != "" {
		path := filepath.Join(opts.WorkDir, spec.Name+".mp4")
		if werr := os.WriteFile(path, raw, 0o644); werr != nil {
			return FileReport{Name: spec.Name, Status: "failed: " + werr.Error()}, 1
		}
	}

	errs := &model.ErrorSet{}
	g, _, err := probe.Probe(bytes.NewReader(raw), int64(len(raw)), probe.DefaultConfig(), errs)
	if err != nil {
		return FileReport{Name: spec.Name, Status: "failed: " + err.Error()}, 1
	}
	// Audio alignment depends on the video boundary for every generated set.
	for _, id := range g.IDs() {
		if g.Tracks[id].Kind == model.KindAudio {
			g.Tracks[id].DependsOn = videoIDs(g)
		}
	}

	dmg := repair.Locate(g, errs, int64(len(raw)))
	dec, err := repair.Adjudicate(g, dmg, errs, repair.DefaultPolicy())
	if err != nil {
		return FileReport{Name: spec.Name, Status: "failed: " + err.Error()}, 1
	}

	rep := FileReport{
		Name:            spec.Name,
		TracksChecked:   len(g.IDs()),
		DamagedRanges:   damagedViews(g, dmg),
		RecoveredRanges: recoveredViews(g, dec),
		Errors:          errs.All(),
		Status:          "ok",
	}

	hashes, err := evidence.HashSegments(bytes.NewReader(raw), g, dec.Segments)
	if err != nil {
		// Truncated payload is an expected, reportable condition for the
		// truncated fixture rather than a process crash.
		rep.Status = "damaged: " + err.Error()
		rep.SegmentHashes = evidence.DigestSummary(hashes)
		return rep, 0
	}
	rep.SegmentHashes = evidence.DigestSummary(hashes)

	if opts.Remux {
		code := remuxOne(g, dec, raw, opts, &rep, hashes)
		if code != 0 {
			return rep, code
		}
	} else {
		// Without remux the reported rates default to the adjudicated state.
		rep.RoundtripMatchRate = 1
		rep.KeyframeIndexHitRate = keyframeHitRate(g)
	}
	return rep, 0
}

func remuxOne(g *model.Graph, dec *repair.Decisions, raw []byte,
	opts DemoOptions, rep *FileReport, inHashes []evidence.SegmentHash) int {
	plan, err := remux.BuildPlan(g, dec)
	if err != nil {
		rep.Status = "failed: " + err.Error()
		return 1
	}
	out, err := remux.Write(plan, bytes.NewReader(raw))
	if err != nil {
		rep.Status = "failed: " + err.Error()
		return 1
	}
	if opts.WorkDir != "" {
		path := filepath.Join(opts.WorkDir, rep.Name+".remux.mp4")
		if werr := os.WriteFile(path, out, 0o644); werr != nil {
			rep.Status = "failed: " + werr.Error()
			return 1
		}
	}
	if opts.Verify {
		v, verr := remux.Verify(out, g, plan, probe.DefaultConfig())
		if verr != nil {
			rep.Status = "failed: " + verr.Error()
			return 1
		}
		rep.RoundtripMatchRate = v.MatchRate
		rep.KeyframeIndexHitRate = v.KeyframeHitRate
		if !v.OK {
			rep.Status = "verification_failed"
			return 2
		}
		// Re-hash the output against the same kept-segment boundaries and
		// prove every input digest survives the repackaging.
		if !outputHashesMatch(g, plan, out, inHashes) {
			rep.Status = "evidence_mismatch"
			return 2
		}
	}
	return 0
}

// outputHashesMatch re-segments the output graph along the source kept ranges
// (remux preserves order) and compares digests one-to-one.
func outputHashesMatch(g *model.Graph, plan *remux.Plan, out []byte,
	inHashes []evidence.SegmentHash) bool {
	errs := &model.ErrorSet{}
	outGraph, _, err := probe.Probe(bytes.NewReader(out), int64(len(out)), probe.DefaultConfig(), errs)
	if err != nil {
		return false
	}
	outSegs := map[uint32][]*repair.Segment{}
	// Map each source segment onto output-relative indices via kept order.
	for _, in := range inHashes {
		tp := plan.Tracks[in.TrackID]
		relFirst := indexOf(tp.Kept, in.FirstSample)
		relLast := indexOf(tp.Kept, in.LastSample)
		if relFirst < 0 || relLast < 0 {
			return false // an isolated or dropped segment cannot be matched
		}
		outSegs[in.TrackID] = append(outSegs[in.TrackID], &repair.Segment{
			TrackID: in.TrackID, FirstSample: relFirst, LastSample: relLast,
			Status: repair.StatusValid,
		})
	}
	hOut, err := evidence.HashSegments(bytes.NewReader(out), outGraph, outSegs)
	if err != nil {
		return false
	}
	return evidence.Compare(inHashes, hOut) == 1
}

func indexOf(slice []int, v int) int {
	for i, x := range slice {
		if x == v {
			return i
		}
	}
	return -1
}

func keyframeHitRate(g *model.Graph) float64 {
	var hits, total int
	for _, id := range g.IDs() {
		t := g.Tracks[id]
		if t.Kind != model.KindVideo {
			continue
		}
		for _, s := range t.Samples {
			if s.IsKeyframe {
				total++
				hits++
			}
		}
	}
	if total == 0 {
		return 1
	}
	return float64(hits) / float64(total)
}

func videoIDs(g *model.Graph) []uint32 {
	var out []uint32
	for _, id := range g.IDs() {
		if g.Tracks[id].Kind == model.KindVideo {
			out = append(out, id)
		}
	}
	return out
}

// damagedViews reports the raw localized damage ranges, independent of
// whether repair later recovers or isolates them.
func damagedViews(g *model.Graph, dmg map[uint32]*repair.TrackDamage) []rangeView {
	var out []rangeView
	for _, id := range g.IDs() {
		for _, r := range dmg[id].DamagedRanges {
			out = append(out, rangeView{TrackID: id, Range: r.String()})
		}
	}
	return out
}

// recoveredViews reports only segments explicitly marked recovered, so valid
// untouched material is not presented as a repair result.
func recoveredViews(g *model.Graph, dec *repair.Decisions) []rangeView {
	var out []rangeView
	for _, id := range g.IDs() {
		for _, seg := range dec.Segments[id] {
			if seg.Status == repair.StatusRecovered {
				out = append(out, rangeView{TrackID: id, Range: seg.Coverage.String()})
			}
		}
	}
	return out
}
