// Package pipeline composes the reusable demux, maptrack, timeline, remux and
// report packages into one deterministic end-to-end processing flow. The CLI
// only drives this package; it contains no processing logic of its own.
package pipeline

import (
	"bytes"
	"os"
	"path/filepath"

	"mediapack/internal/demux"
	"mediapack/internal/fixture"
	"mediapack/internal/maptrack"
	"mediapack/internal/model"
	"mediapack/internal/remux"
	"mediapack/internal/report"
	"mediapack/internal/timeline"
)

// Result is everything a run produces, including a ready-to-print summary and
// the exit code implied by the outcome.
type Result struct {
	Summary    *report.Summary
	ExitCode   int
	OutputPath string
}

// NormalRun processes the generated MXF and MP4 inputs through the full
// pipeline and writes one repackaged output. It never returns an error for an
// expected, locatable input failure; that is captured in the summary and exit
// code instead.
func NormalRun(set *fixture.DemoSet, outPath string) (*Result, error) {
	mxfC, err1 := parseMXF(set.MXFPath, set.MXFRaw)
	if err1 != nil {
		return failure(err1, outPath), nil
	}
	mp4C, err2 := parseMP4(set.MP4Path, set.MP4Raw)
	if err2 != nil {
		return failure(err2, outPath), nil
	}
	containers := []*demux.Container{mxfC, mp4C}
	// Normalize every container-relative path to its basename so provenance
	// and output bytes never depend on the (possibly different) --out dir.
	// This is what makes reruns into distinct directories byte-identical.
	for _, c := range containers {
		normalizeContainer(c)
	}
	rules := convertRules(append(append([]fixture.Rule{}, set.MXFRules...),
		set.MP4Rules...))
	for i := range rules {
		rules[i].SourcePath = filepath.Base(rules[i].SourcePath)
	}

	mapping, err := maptrack.Build(containers, rules, maptrack.StandardDefaults())
	if err != nil {
		return failure(err, outPath), nil
	}

	tl := buildTimeline(mapping)
	mf, err := remux.Write(mapping, outPath)
	if err != nil {
		return failure(err, outPath), nil
	}

	sum := report.Build(report.Inputs{
		Mapping: mapping, Timeline: tl, Manifest: mf, OutputPath: outPath,
	})
	sum.AttachManifestOffsets(mf)
	code := 0
	if sum.ByteDiff != 0 {
		code = 3
	}
	return &Result{Summary: sum, ExitCode: code, OutputPath: outPath}, nil
}

// CorruptRun processes only the corrupted input and reports the first illegal
// offset with the input/mapping exit code (2).
func CorruptRun(set *fixture.DemoSet, outPath string) *Result {
	_, err := parseMP4(set.CorruptPath, set.CorruptRaw)
	if err == nil {
		// A corrupted input that parses cleanly is itself a verification fault.
		return &Result{
			Summary: report.Build(report.Inputs{Err: model.NewError(
				model.CodeVerify, "corrupt input unexpectedly parsed")}),
			ExitCode: 3,
		}
	}
	return failure(err, outPath)
}

// RecoveryRun demonstrates wrap expansion plus a late-event replay and
// verifies the replayed boundaries equal a full event-time recomputation.
func RecoveryRun(sc fixture.RecoveryScenario) (*Result, error) {
	rule := timeline.StandardRule()
	tl := timeline.New(rule, 3)
	for _, e := range sc.Events {
		if _, err := tl.Ingest(convertEvent(e)); err != nil {
			return nil, err
		}
	}
	lateBoundary, err := tl.Ingest(convertEvent(sc.Late))
	if err != nil {
		return nil, err
	}

	all := append(append([]fixture.TimedEvent{}, sc.Events...), sc.Late)
	oracle, err := fullOracle(rule, all)
	if err != nil {
		return nil, err
	}
	got := tl.Boundaries()
	if !boundariesEqual(got, oracle) {
		return &Result{
			Summary: report.Build(report.Inputs{Err: model.NewError(
				model.CodeVerify, "replay diverged from full recompute")}),
			ExitCode: 3,
		}, nil
	}

	sum := report.Build(report.Inputs{Timeline: tl})
	sum.Status = "ok"
	_ = lateBoundary
	return &Result{Summary: sum, ExitCode: 0}, nil
}

func failure(err error, outPath string) *Result {
	sum := report.Build(report.Inputs{Err: err})
	code := 2
	if model.CodeOf(err) == model.CodeVerify {
		code = 3
	}
	return &Result{Summary: sum, ExitCode: code, OutputPath: outPath}
}

func parseMXF(path string, raw []byte) (*demux.Container, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return nil, err
	}
	return demux.MXF(path, bytes.NewReader(raw), int64(len(raw)))
}

func parseMP4(path string, raw []byte) (*demux.Container, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return nil, err
	}
	return demux.MP4(path, bytes.NewReader(raw), int64(len(raw)))
}

func convertRules(in []fixture.Rule) []maptrack.Rule {
	out := make([]maptrack.Rule, len(in))
	for i, r := range in {
		out[i] = maptrack.Rule{
			SourcePath: r.SourcePath, SourceTrack: r.SourceTrack,
			TargetTrack: r.TargetTrack, Role: r.Role,
		}
	}
	return out
}

// normalizeContainer rewrites the container path and every sample's source to
// the file basename, removing absolute-directory dependence from the output.
func normalizeContainer(c *demux.Container) {
	base := filepath.Base(c.Path)
	c.Path = base
	for tid, samples := range c.Samples {
		for i := range samples {
			samples[i].Source = base
		}
		c.Samples[tid] = samples
	}
}

func convertEvent(e fixture.TimedEvent) timeline.Event {
	return timeline.Event{
		Seq: e.Seq, Source: e.Source, TrackID: e.TrackID,
		SampleIndex: e.SampleIndex, OriginalTick: e.OriginalTick,
		Duration: e.Duration, RTPRaw: e.RTPRaw, HasRTP: e.HasRTP, Late: e.Late,
	}
}

// buildTimeline feeds every mapped sample into a fresh timeline so the summary
// can report repair statistics over the real mapped data.
func buildTimeline(m *maptrack.Mapping) *timeline.Timeline {
	tl := timeline.New(timeline.StandardRule(), 8)
	var seq uint64
	for _, b := range m.Bindings {
		for _, s := range b.Samples {
			seq++
			_, _ = tl.Ingest(timeline.Event{
				Seq: seq, Source: s.Source, TrackID: s.TargetTrack,
				SampleIndex: s.Index, OriginalTick: s.DTS,
				Duration: s.Duration, RTPRaw: s.RTPRaw,
				HasRTP: s.RTPCycles != 0 || s.RTPRaw != 0,
			})
		}
	}
	return tl
}

func fullOracle(rule timeline.Rule, all []fixture.TimedEvent,
) ([]model.Boundary, error) {
	evs := make([]timeline.Event, len(all))
	for i, e := range all {
		evs[i] = convertEvent(e)
	}
	return timeline.FullRecomputeFromScratch(rule, 3, evs)
}

func boundariesEqual(a, b []model.Boundary) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].OriginalTick != b[i].OriginalTick ||
			a[i].RepairedTick != b[i].RepairedTick || a[i].Delta != b[i].Delta {
			return false
		}
	}
	return true
}
