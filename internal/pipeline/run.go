package pipeline

import (
	"encoding/hex"
	"path/filepath"

	"replay/internal/cache"
	"replay/internal/clip"
	"replay/internal/events"
	"replay/internal/manifest"
	"replay/internal/media"
)

// plannedEvent is the plan-stage result for one event.
type plannedEvent struct {
	ev        *events.AlignedEvent
	spec      clip.ClipSpec
	decision  events.Decision
	why       string
	anomaly   *media.AnomalyInterval
	clipDur   int64 // video ticks after keyframe alignment
	deviation float64
	videoTS   uint32
}

// Run executes the four stages with checkpointed recovery. Exit codes are
// decided by the caller via the returned error kind.
func Run(c *Config) (*Stats, *manifest.Manifest, error) {
	if err := validate(c); err != nil {
		return nil, nil, err
	}
	inputSHA, inputSize, err := hashFile(c.Input)
	if err != nil {
		return nil, nil, err
	}
	feed, err := events.Load(c.Events, c.RTPClock, c.RTPAnchor, c.MinConfidence)
	if err != nil {
		return nil, nil, err
	}
	eventsSHA := hex.EncodeToString(feed.SHA[:])
	configSHA := cache.ConfigDigest(c.ConfigMap())

	cpStore, err := NewStore(filepath.Join(c.OutDir, ".state"))
	if err != nil {
		return nil, nil, err
	}
	cp, err := cpStore.Load()
	if err != nil {
		return nil, nil, err
	}
	if cp != nil {
		if mErr := cp.Matches(inputSHA, eventsSHA, configSHA, inputSize); mErr != nil {
			// Different inputs/config: old state is irrelevant, start over.
			_ = cpStore.Clear()
			cp = nil
		}
	}
	resumed := cp != nil
	if cp == nil {
		cp = &Checkpoint{
			Version: 1, Stage: StageNone, InputSHA: inputSHA,
			EventsSHA: eventsSHA, ConfigSHA: configSHA, InputSize: inputSize,
			UpdatedAt: "deterministic",
		}
		if err := cpStore.Save(cp); err != nil {
			return nil, nil, err
		}
	}

	st := &Stats{
		RTPBad: feed.Stats.Bad, RTPWraps: feed.Stats.Wraps,
		BudgetS: c.Budget.Seconds(),
	}
	var fi *media.FileInfo
	var tl *media.Timeline
	var plans []*plannedEvent
	var results []*clip.ClipResult

	// ---- scan: parse container and build timelines ----
	if !cp.Reached(StageScan) {
		fi, err = media.ParseFile(c.Input)
		if err != nil {
			return st, nil, err
		}
		tl, err = media.Build(fi)
		if err != nil {
			return st, nil, err
		}
		st.AnomalyCount = countAnomalies(tl)
		if err := cpStore.Advance(cp, StageScan); err != nil {
			return st, nil, err
		}
	} else {
		fi, err = media.ParseFile(c.Input)
		if err != nil {
			return st, nil, err
		}
		tl, err = media.Build(fi)
		if err != nil {
			return st, nil, err
		}
		st.AnomalyCount = countAnomalies(tl)
	}
	if c.StopAfter == StageScan {
		return st, nil, &StopAfter{stage: StageScan}
	}

	// ---- plan: map, judge and budget every event ----
	if !cp.Reached(StagePlan) {
		plans = planEvents(c, tl, feed, st)
		cp.PlannedCount = len(plans)
		if err := cpStore.Advance(cp, StagePlan); err != nil {
			return st, nil, err
		}
	} else {
		plans = planEvents(c, tl, feed, st)
	}
	if c.StopAfter == StagePlan {
		return st, nil, &StopAfter{stage: StagePlan}
	}

	// ---- cut: rebuild accepted clips, cache-verified ----
	cacheStore, err := cache.Open(c.CacheDir)
	if err != nil {
		return st, nil, err
	}
	builder, err := clip.NewBuilder(fi, tl)
	if err != nil {
		return st, nil, err
	}
	if !cp.Reached(StageCut) {
		results, err = cutPlans(c, builder, cacheStore, plans, inputSHA, configSHA, st)
		if err != nil {
			return st, nil, err
		}
		cp.CutCount = len(results)
		if err := cpStore.Advance(cp, StageCut); err != nil {
			return st, nil, err
		}
	} else {
		results, err = cutPlans(c, builder, cacheStore, plans, inputSHA, configSHA, st)
		if err != nil {
			return st, nil, err
		}
	}
	if c.StopAfter == StageCut {
		return st, nil, &StopAfter{stage: StageCut}
	}

	// ---- commit: write manifest only after every clip is verified ----
	mf := buildManifest(c, fi, tl, feed, plans, results, st, inputSHA)
	if err := manifest.Write(filepath.Join(c.OutDir, "manifest.json"), mf); err != nil {
		return st, nil, err
	}
	if !cp.Reached(StageCommit) {
		if err := cpStore.Advance(cp, StageCommit); err != nil {
			return st, nil, err
		}
	}
	_ = resumed
	return st, mf, nil
}

func countAnomalies(tl *media.Timeline) int {
	n := 0
	for _, t := range tl.Tracks {
		n += len(t.Anomalies)
	}
	return n
}

// buildManifest assembles the deterministic delivery document from the
// plan verdicts, cut results and timeline anomalies.
func buildManifest(c *Config, fi *media.FileInfo, tl *media.Timeline,
	feed *events.Feed, plans []*plannedEvent, results []*clip.ClipResult,
	st *Stats, inputSHA string) *manifest.Manifest {

	resultByID := map[string]*clip.ClipResult{}
	for _, r := range results {
		resultByID[r.EventID] = r
	}
	eventRecords := make([]manifest.EventRecord, 0, len(plans))
	for _, pe := range plans {
		rec := manifest.EventRecord{
			ID:          pe.ev.ID,
			Decision:    string(pe.decision),
			MediaTick:   pe.ev.TimeTick,
			Timescale:   pe.ev.Timescale,
			Confidence:  pe.ev.Confidence,
			RejectWhy:   pe.why,
			DuplicateOf: pe.ev.DuplicateOf,
		}
		if pe.anomaly != nil {
			rec.Anomaly = &manifest.AnomalyRecord{
				Kind: string(pe.anomaly.Kind), TrackID: pe.anomaly.TrackID,
				StartTick: pe.anomaly.StartTick, EndTick: pe.anomaly.EndTick,
				StartIndex: pe.anomaly.StartIndex, EndIndex: pe.anomaly.EndIndex,
				Detail: pe.anomaly.Detail,
			}
		}
		// Only the accepted (first) occurrence of an ID owns a clip;
		// duplicates and skips reference the decision without artifacts.
		if r, ok := resultByID[pe.ev.ID]; ok && pe.decision == events.DecisionAccepted {
			cr := &manifest.ClipRecord{
				FileName: r.FileName, SHA256: r.SHA256Hex, Size: r.Size,
				StartPTS: r.StartPTS, EndPTS: r.EndPTS,
				DurationTicks: r.Duration, Timescale: r.Timescale,
				VideoFirst: r.VideoRange.First, VideoLast: r.VideoRange.Last,
			}
			for _, s := range r.SampleOff {
				cr.SampleOffsets = append(cr.SampleOffsets, manifest.SampleRecord{
					TrackID: s.TrackID, OutputIndex: s.OutputIndex,
					SourceIndex: s.SourceIndex, SourceOffset: s.SourceOffset, Size: s.Size,
				})
			}
			rec.Clip = cr
		}
		eventRecords = append(eventRecords, rec)
	}
	var anomalies []manifest.AnomalyRecord
	for _, track := range tl.Tracks {
		for _, a := range track.Anomalies {
			anomalies = append(anomalies, manifest.AnomalyRecord{
				Kind: string(a.Kind), TrackID: a.TrackID,
				StartTick: a.StartTick, EndTick: a.EndTick,
				StartIndex: a.StartIndex, EndIndex: a.EndIndex, Detail: a.Detail,
			})
		}
	}
	mf := &manifest.Manifest{
		SchemaVersion: 1,
		Source: manifest.SourceRecord{
			InputPath: c.Input, InputSHA: inputSHA, InputSize: fi.Size,
			EventsSHA: hex.EncodeToString(feed.SHA[:]),
			ConfigSHA: cache.ConfigDigest(c.ConfigMap()),
		},
		Events: eventRecords, Anomalies: anomalies,
		Stats: manifest.Stats{
			Accepted: st.Accepted, Duplicate: st.Duplicate,
			SkipBudget: st.SkipBudget, SkipConfidence: st.SkipConfidence,
			SkipUntrusted: st.SkipUntrusted, NoKeyframe: st.NoKeyframe,
			RTPBad: st.RTPBad, RTPWraps: st.RTPWraps,
			AnomalyCount: st.AnomalyCount, Affected: st.Affected,
			ClipS: round3(st.ClipS), BudgetS: round3(st.BudgetS),
			DeviationS: round3(st.DeviationS),
		},
	}
	return mf
}

// round3 rounds to milliseconds; the CLI drops the decimal for integral
// values, while the manifest keeps millisecond precision deterministically.
func round3(v float64) float64 {
	if v >= 0 {
		return float64(int64(v*1000+0.5)) / 1000
	}
	return float64(int64(v*1000-0.5)) / 1000
}

