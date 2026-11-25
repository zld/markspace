package pipeline

import (
	"replaycheck/internal/ingest"
	"replaycheck/internal/model"
	"replaycheck/internal/report"
)

func init() { register(model.StageIngest, (*run).stageIngest) }

func (r *run) stageIngest() error {
	input := model.Fingerprint("ingest", r.mediaAsset.Digest, r.eventAsset.Digest)
	var sum ingestSummary
	if out, ok := r.fetchIfSkipped(model.StageIngest, "ingest", input, &sum); ok {
		r.ingestOut = out
		r.record(model.StageIngest, input, true)
		r.finish(model.StageIngest, out)
		return nil
	}
	cached := r.fetch("ingest", input, &sum)
	if !cached {
		cont, err := ingest.ParseContainer(r.mediaAsset.Path, r.cfg.Limits)
		if err != nil {
			return err
		}
		events, err := ingest.ParseEvents(r.eventAsset.Path, r.clockRate)
		if err != nil {
			return err
		}
		r.cont, r.events = cont, events
		sum = summarize(cont, events)
		if err := r.save("ingest", input, sum); err != nil {
			return err
		}
	}
	r.ingestOut, _ = model.DigestJSON(sum)
	r.result.WrapCount = sum.Wraps
	r.record(model.StageIngest, input, cached)
	r.out.Emit(report.KindTimeline, map[string]any{
		"phase": "ingest", "mediaDigest": sum.MediaDigest, "eventDigest": sum.EventDigest,
		"boxes": sum.BoxCount, "tracks": len(sum.Tracks), "events": sum.Events, "wraps": sum.Wraps,
	})
	r.finish(model.StageIngest, r.ingestOut)
	return nil
}

func summarize(c *ingest.Container, ev *ingest.EventFile) ingestSummary {
	s := ingestSummary{MediaDigest: c.Digest, BoxCount: c.BoxCount, MediaSize: c.Size}
	if ev != nil {
		s.EventDigest = ev.Digest
		for _, st := range ev.Streams {
			s.Events += len(st.Events)
			s.Wraps += st.Wraps
		}
	}
	for _, t := range c.Tracks {
		s.Tracks = append(s.Tracks, trackSummary{
			ID: t.ID, Kind: t.Kind, Codec: t.Codec, Timescale: t.Timescale,
			Duration: t.Duration, Samples: t.SampleCount, Bytes: t.MediaBytes,
		})
	}
	return s
}
