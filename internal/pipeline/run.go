package pipeline

import (
	"continuity/internal/clock"
	"continuity/internal/index"
	"continuity/internal/manifest"
	"continuity/internal/media"
)

// Run executes the five stages in order, honoring resume state. A fault
// injection returns *index.InjectedCrash (mapped to exit 75 by the CLI);
// invalid/unrecoverable inputs return structured errors (exit 2).
func (r *Runner) Run() (*Report, error) {
	if err := r.stageParse(); err != nil {
		return nil, err
	}
	if err := r.stageAlign(); err != nil {
		return nil, err
	}
	if err := r.stageVerify(); err != nil {
		return nil, err
	}
	if err := r.stageWrite(); err != nil {
		return nil, err
	}
	return r.stagePublish()
}

// Verify re-reads a completed run and recomputes the acceptance report
// without modifying any state. It fails if publish never completed.
func (r *Runner) Verify() (*Report, error) {
	if !r.journal.IsStageDone(index.StagePublish) {
		return nil, clock.New(clock.KindInvalidInput, "verify",
			"run never reached publish; nothing to verify")
	}
	if err := r.loadIndex(); err != nil {
		return nil, err
	}
	if err := r.parseManifests(); err != nil {
		return nil, err
	}
	if err := r.loadFrameMeta(); err != nil {
		return nil, err
	}
	if err := r.rebuildTimelines(); err != nil {
		return nil, err
	}
	if err := r.inspectForVerify(); err != nil {
		return nil, err
	}
	return r.buildReport()
}

// inspectForVerify populates r.verified without touching journal stage state.
func (r *Runner) inspectForVerify() error {
	r.verified = nil
	for repID, tl := range r.timelines {
		for _, seg := range tl.Segments {
			full, err := manifest.SafePath(r.manifestDir(seg), seg.URL)
			if err != nil {
				return err
			}
			info, err := media.InspectSegment(full)
			if err != nil {
				return clock.AtFile(clock.AtLine(err, seg.Line), full)
			}
			r.verified = append(r.verified, inspectedSegment{
				repID: repID, seg: seg, info: info,
			})
		}
	}
	return nil
}
