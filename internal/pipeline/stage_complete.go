package pipeline

import (
	"replaycheck/internal/model"
	"replaycheck/internal/report"
)

func init() { register(model.StageComplete, (*run).stageComplete) }

func (r *run) stageComplete() error {
	input := model.Fingerprint("complete", r.manifestOut, r.mediaAsset.Digest)
	r.record(model.StageComplete, input, false)
	r.result.Stages = len(r.runner.State.Records)
	if r.resumeAt != "" && r.resumeAt != model.StageIngest {
		r.result.ResumedFrom = string(r.resumeAt)
	}
	r.out.Emit(report.KindSummary, r.result)
	r.finish(model.StageComplete, r.result.ManifestDigest)
	return nil
}
