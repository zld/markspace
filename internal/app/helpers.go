package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"rateqa/internal/control"
	"rateqa/internal/jobs"
	"rateqa/internal/store"
)

// readCheckpoint loads the persisted checkpoint without replaying the journal.
func readCheckpoint(path string) (store.Checkpoint, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return store.Checkpoint{}, fmt.Errorf("checkpoint missing or unreadable (%w); run demo/run first", err)
	}
	var cp store.Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return store.Checkpoint{}, fmt.Errorf("corrupt checkpoint: %w", err)
	}
	return cp, nil
}

// specsFromWorkspace rebuilds segment specs from the stored segment files.
func specsFromWorkspace(workspace string, params store.Parameters) ([]jobs.SegmentSpec, error) {
	segDir := filepath.Join(workspace, "segments")
	refs, err := splitIfNeeded("", segDir, params.FramesPerSegment)
	if err != nil {
		return nil, err
	}
	if len(refs) != params.Segments {
		return nil, fmt.Errorf("segment count changed: checkpoint has %d, workspace has %d",
			params.Segments, len(refs))
	}
	specs := make([]jobs.SegmentSpec, len(refs))
	for i, ref := range refs {
		specs[i] = jobs.SegmentSpec{Ref: ref, TargetKbps: params.TargetKbps}
	}
	return specs, nil
}

// resultsFromStore reconstructs scheduler-style results purely from completed
// journal jobs, used by the report subcommand without re-running ffmpeg.
func resultsFromStore(st *store.Store, specs []jobs.SegmentSpec) []jobs.SegmentResult {
	snap := st.Snapshot()
	bySeg := map[int][]store.Job{}
	for _, j := range snap {
		if j.State == store.StateCompleted {
			bySeg[j.Segment] = append(bySeg[j.Segment], j)
		}
	}
	out := make([]jobs.SegmentResult, 0, len(specs))
	for _, spec := range specs {
		segJobs := bySeg[spec.Ref.Index]
		if len(segJobs) == 0 {
			continue
		}
		search := control.NewSegmentSearch(spec.TargetKbps)
		for _, j := range segJobs {
			search.Observe(control.Sample{
				QP: j.QP, BitrateKbps: j.Metrics.BitrateKbps, PSNRdB: j.Metrics.PSNRMean,
			})
		}
		best, err := search.Best()
		if err != nil {
			continue
		}
		out = append(out, jobs.SegmentResult{
			Index: spec.Ref.Index, Selected: best, Bounded: search.Bounded(),
			Reason: search.Reason(), Rounds: search.Rounds(), Candidates: search.Samples(),
		})
	}
	return out
}
