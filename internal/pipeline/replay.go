package pipeline

import (
	"bytes"
	"fmt"

	"medcheck/internal/fixture"
	"medcheck/internal/index"
	"medcheck/internal/model"
	"medcheck/internal/repair"
	"medcheck/internal/report"
	"medcheck/internal/timeline"
)

// derive builds the full derived artifact set for one arrival order.
func derive(frames []*model.Frame, cfg Config) (*timeline.Engine, *report.Summary, *index.KeyframeIndex, []*repair.Segment) {
	eng := timeline.NewEngine(cfg.LateWindowMicros)
	for _, f := range frames {
		eng.Push(f)
	}
	view := eng.View()
	idx := index.Build(view.ValidFrames())
	segs := repair.Build(view.Ordered)
	sum := report.Build(report.Inputs{
		View: view, Index: idx, Segments: segs, WindowMicros: cfg.ShardWindowMicros,
	})
	return eng, sum, idx, segs
}

// runLateReplay feeds the same late frames in forward and reverse arrival order
// and proves the derived summary, index and shard hashes are identical.
func runLateReplay(res *Result, cfg Config) error {
	base, late := fixture.LateFrames()

	forwardFrames := make([]*model.Frame, 0, len(base.Frames)+len(late))
	forwardFrames = append(forwardFrames, base.Frames...)
	for _, f := range late { // late frames in forward order 100,101
		forwardFrames = append(forwardFrames, f)
	}
	reverseFrames := make([]*model.Frame, 0, len(base.Frames)+len(late))
	reverseFrames = append(reverseFrames, base.Frames...)
	for i := len(late) - 1; i >= 0; i-- { // late frames reversed 101,100
		reverseFrames = append(reverseFrames, late[i])
	}

	engF, sumF, idxF, _ := derive(forwardFrames, cfg)
	engR, sumR, idxR, _ := derive(reverseFrames, cfg)
	res.RecomputeForward = engF.RecomputeCount()
	res.RecomputeReverse = engR.RecomputeCount()

	idxEqual := indexFingerprint(idxF) == indexFingerprint(idxR)
	shardEqual := equalShards(
		report.BuildShards(engF.View().Ordered, cfg.ShardWindowMicros),
		report.BuildShards(engR.View().Ordered, cfg.ShardWindowMicros))
	res.SummaryHashForward = sumF.Hash()
	res.SummaryHashReverse = sumR.Hash()
	res.ReplayEqual = sumF.Hash() == sumR.Hash() && idxEqual && shardEqual
	if !res.ReplayEqual {
		return fmt.Errorf("replay mismatch: summary %t index %t shards %t",
			sumF.Hash() == sumR.Hash(), idxEqual, shardEqual)
	}
	return nil
}

func indexFingerprint(idx *index.KeyframeIndex) string {
	var b bytes.Buffer
	for _, e := range idx.Entries() {
		fmt.Fprintf(&b, "%d/%d/%d@%d#%d;", e.Track, e.Source, e.Seq, e.Micros, e.Offset.Start)
	}
	return b.String()
}

func equalShards(a, b []report.Shard) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].SHA256 != b[i].SHA256 || a[i].Bucket != b[i].Bucket || a[i].Track != b[i].Track {
			return false
		}
	}
	return true
}
