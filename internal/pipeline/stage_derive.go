package pipeline

import (
	"os"

	"replaycheck/internal/derive"
	"replaycheck/internal/model"
	"replaycheck/internal/policy"
	"replaycheck/internal/report"
)

func init() { register(model.StageDerive, (*run).stageDerive) }

type clipRecord struct {
	Segment    int        `json:"segment"`
	Digest     string     `json:"digest"`
	Bytes      int64      `json:"bytes"`
	Level      string     `json:"level"`
	QualityPPM policy.PPM `json:"qualityPpm"`
	Truncated  bool       `json:"truncated"`
}

type deriveRecord struct {
	Policy policy.Result `json:"policy"`
	Clips  []clipRecord  `json:"clips"`
}

func (r *run) policyDigest() (string, error) {
	return r.cfg.Budget.Digest(policy.DefaultLadder())
}

func (r *run) stageDerive() error {
	polDigest, err := r.policyDigest()
	if err != nil {
		return model.WrapError(model.StageDerive, "policy", model.TrackRef{}, 0, "cannot digest policy", err)
	}
	input := model.Fingerprint("derive", r.splitOut, polDigest)
	var rec deriveRecord
	if out, ok := r.fetchIfSkipped(model.StageDerive, "derive", input, &rec); ok {
		r.restoreDerive(rec)
		r.deriveOut = out
		r.record(model.StageDerive, input, true)
		r.emitDerive()
		r.finish(model.StageDerive, out)
		return nil
	}
	cached := r.fetch("derive", input, &rec)
	if !cached {
		if r.plan == nil {
			if err := r.stageSplit(); err != nil {
				return err
			}
		}
		res, conflict, err := policy.Decide(r.plan.Segments, r.cfg.Budget, policy.DefaultLadder())
		if err != nil {
			return err
		}
		if conflict != nil {
			r.out.Emit(report.KindDerive, map[string]any{"conflict": conflict})
			return model.Errorf(model.StageDerive, "budget", model.TrackRef{}, 0,
				"budget conflict on %s: requested %d, achievable %d", conflict.Field, conflict.Requested, conflict.Achievable)
		}
		src, err := os.Open(r.mediaAsset.Path)
		if err != nil {
			return model.WrapError(model.StageDerive, "open", model.TrackRef{}, 0, "cannot open sealed source", err)
		}
		d := &derive.Deriver{
			Source: src, SourceDigest: r.mediaAsset.Digest,
			Blobs: r.blobs, Cache: r.cache, PolicyDigest: polDigest,
		}
		clips, derr := d.Derive(r.plan, res)
		src.Close()
		if derr != nil {
			return derr
		}
		rec = deriveRecord{Policy: *res}
		for _, c := range clips {
			rec.Clips = append(rec.Clips, clipRecord{
				Segment: c.Segment, Digest: c.Digest, Bytes: c.Bytes,
				Level: c.Level, QualityPPM: c.QualityPPM, Truncated: c.Truncated,
			})
		}
		if err := r.save("derive", input, rec); err != nil {
			return err
		}
		r.pres = res
		r.clips = clips
	} else {
		r.restoreDerive(rec)
	}
	r.deriveOut, _ = model.DigestJSON(rec)
	r.record(model.StageDerive, input, cached)
	r.emitDerive()
	r.finish(model.StageDerive, r.deriveOut)
	return nil
}

func (r *run) restoreDerive(rec deriveRecord) {
	res := rec.Policy
	r.pres = &res
	r.clips = nil
	for _, c := range rec.Clips {
		r.clips = append(r.clips, derive.Clip{
			Segment: c.Segment, Digest: c.Digest, Bytes: c.Bytes, Level: c.Level,
			QualityPPM: c.QualityPPM, Truncated: c.Truncated, Cached: true,
		})
	}
}

func (r *run) emitDerive() {
	if r.pres == nil {
		return
	}
	r.result.CacheHits = r.cache.Stats().Hits
	r.out.Emit(report.KindDerive, map[string]any{
		"clips": r.clips, "totalBytes": r.pres.TotalBytes,
		"totalProcMs": r.pres.TotalProcMS, "minQualityPpm": r.pres.MinQualityPPM,
		"cacheHits": r.cache.Stats().Hits,
	})
}
