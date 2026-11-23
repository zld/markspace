package pipeline

import (
	"archivist/internal/container"
	"archivist/internal/dag"
	"archivist/internal/model"
	"archivist/internal/segmenter"
	"archivist/internal/timeline"
)

// rerunStable models the DAG idempotency guarantee: the same manifest digest
// must be produced on a second run without re-committing any artifact.
func (r *Runner) rerunStable(
	fix *container.Fixture, pf *container.ParsedFile, first *segmenter.Manifest,
	decisions []model.Decision, warnings []model.Warning,
) bool {
	nodes := r.dagNodes(fix.Digest)
	g, err := dag.NewGraph(nodes)
	if err != nil {
		return false
	}
	runner := dag.NewRunner(g, dag.Config{MaxRetries: r.cfg.Retries})
	rep, err := runner.Execute()
	if err != nil || rep.FirstBlocker != nil {
		return false
	}
	// Recompute the manifest digest from identical inputs and compare.
	tcs := r.videoTrackClocks(pf)
	alignment, err := timeline.AlignBoundaries(tcs, r.requiredSegments(pf))
	if err != nil || !alignment.Aligned {
		return false
	}
	plan, err := segmenter.Cut(pf.Tracks, r.representations(tcs),
		alignment, r.cfg.SegmentMS, fix.Data)
	if err != nil {
		return false
	}
	second, err := segmenter.BuildManifest(fix.Name, plan, r.representations(tcs),
		decisions, warnings)
	if err != nil {
		return false
	}
	return second.Digest == first.Digest
}

// dagNodes builds the six-stage pipeline graph. Failures are injected only by
// the underlying stage functions; here every node succeeds deterministically.
func (r *Runner) dagNodes(inputDigest string) []*dag.Node {
	ok := func(*dag.RunContext) error { return nil }
	return []*dag.Node{
		{ID: "generate", Stage: model.StageFixture, InputDigest: inputDigest, ParamDigest: r.paramDigest(), Execute: ok},
		{ID: "parse", Stage: model.StageContainer, Deps: []string{"generate"}, InputDigest: inputDigest, ParamDigest: r.paramDigest(), Execute: ok},
		{ID: "index", Stage: model.StageIndex, Deps: []string{"parse"}, InputDigest: inputDigest, ParamDigest: r.paramDigest(), Execute: ok},
		{ID: "align", Stage: model.StageTimeline, Deps: []string{"index"}, InputDigest: inputDigest, ParamDigest: r.paramDigest(), Execute: ok},
		{ID: "segment", Stage: model.StageSegment, Deps: []string{"align"}, InputDigest: inputDigest, ParamDigest: r.paramDigest(), Execute: ok},
		{ID: "manifest", Stage: model.StageManifest, Deps: []string{"segment"}, InputDigest: inputDigest, ParamDigest: r.paramDigest(), Execute: ok},
	}
}

func (r *Runner) paramDigest() string {
	return "seg=" + itoa(uint64(r.cfg.SegmentMS)) +
		"-retry=" + itoa(uint64(r.cfg.Retries)) +
		"-corr=" + r.cfg.Corruption +
		"-seed=" + itoas(r.cfg.Seed)
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func itoas(v int64) string {
	if v < 0 {
		return "-" + itoa(uint64(-v))
	}
	return itoa(uint64(v))
}
