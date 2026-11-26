package pipeline

import (
	"sync"

	"continuity/internal/clock"
	"continuity/internal/index"
	"continuity/internal/manifest"
	"continuity/internal/media"
	"continuity/internal/timeline"
)

// stageAlign builds the unified timeline per representation, filling primary
// gaps only from time-overlapping backup segments.
func (r *Runner) stageAlign() error {
	if r.journal.IsStageDone(index.StageAlign) && r.cfg.Resume {
		return r.rebuildTimelines()
	}
	pairs, err := timeline.PairRepresentations(r.prim, r.backup)
	if err != nil {
		return err
	}
	// Recovered counts distinct recovered program intervals, not the number
	// of representation copies that each carry the same shared timeline gap.
	gapKeys := map[string]bool{}
	for i := range r.prim.Representations {
		prim := &r.prim.Representations[i]
		var back *manifest.Representation
		if b, ok := pairs[prim.ID]; ok {
			back = b
		}
		tl, err := timeline.Build(prim, back, prim.ID)
		if err != nil {
			return err
		}
		for _, g := range tl.Gaps {
			gapKeys[g.Start.String()+"-"+g.End.String()] = true
		}
		r.timelines[prim.ID] = tl
	}
	totalRecovered := len(gapKeys)
	if totalRecovered > 0 {
		if err := r.journal.NoteRecovered(totalRecovered); err != nil {
			return err
		}
	}
	return r.journal.AdvanceStage(index.StageAlign)
}

// rebuildTimelines reconstructs in-memory timelines during resume.
func (r *Runner) rebuildTimelines() error {
	pairs, err := timeline.PairRepresentations(r.prim, r.backup)
	if err != nil {
		return err
	}
	for i := range r.prim.Representations {
		prim := &r.prim.Representations[i]
		var back *manifest.Representation
		if b, ok := pairs[prim.ID]; ok {
			back = b
		}
		tl, err := timeline.Build(prim, back, prim.ID)
		if err != nil {
			return err
		}
		r.timelines[prim.ID] = tl
	}
	return nil
}

// inspectedSegment bundles an inspection result with its timeline position.
type inspectedSegment struct {
	repID string
	seg   timeline.Segment
	info  *media.SegmentInfo
}

// stageVerify inspects every segment on every timeline with at most
// maxWorkers concurrent readers, rejecting corrupt media before any write.
func (r *Runner) stageVerify() error {
	if r.journal.IsStageDone(index.StageVerify) && r.cfg.Resume {
		// Re-inspect to repopulate in-memory results without re-committing.
		return r.inspectForVerify()
	}
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		firstErr error
		sem     = newSemaphore(maxWorkers)
	)
	setErr := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	for repID, tl := range r.timelines {
		for _, seg := range tl.Segments {
			if firstErr != nil {
				break
			}
			wg.Add(1)
			sem.acquire()
			go func(repID string, seg timeline.Segment) {
				defer wg.Done()
				defer sem.release()
				full, err := manifest.SafePath(r.manifestDir(seg), seg.URL)
				if err != nil {
					setErr(err)
					return
				}
				info, err := media.InspectSegment(full)
				if err != nil {
					setErr(clock.AtFile(clock.AtLine(err, seg.Line), full))
					return
				}
				mu.Lock()
				r.verified = append(r.verified, inspectedSegment{
					repID: repID, seg: seg, info: info,
				})
				mu.Unlock()
			}(repID, seg)
		}
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return r.journal.AdvanceStage(index.StageVerify)
}

// manifestDir resolves the directory containing a segment's manifest so
// relative URIs resolve against the correct primary/backup root.
func (r *Runner) manifestDir(seg timeline.Segment) string {
	if seg.Source == timeline.SourceBackup && r.backup != nil {
		return r.backup.Dir
	}
	return r.prim.Dir
}
