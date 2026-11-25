package pipeline

import (
	"encoding/json"
	"sort"

	"regress-kernel/internal/ingest"
	"regress-kernel/internal/media"
)

// RenditionReport is the per-bitrate section of the terminal report.
type RenditionReport struct {
	TaskKey       string              `json:"task_key"`
	Rendition     string              `json:"rendition"`
	SegmentCount  int                 `json:"segment_count"`
	Continuous    bool                `json:"continuous"`
	FrameCounters media.FrameCounters `json:"frame_counters"`
	SyncErrorMs   float64             `json:"sync_error_ms"`
	SampleCount   int                 `json:"sync_sample_count"`
	Records       []ArtifactRecord    `json:"records"`
}

// ProgramResult is the full, JSON-serializable outcome of one program run.
type ProgramResult struct {
	CaseName     string            `json:"case_name"`
	Seed         int64             `json:"seed"`
	Renditions   []RenditionReport `json:"renditions"`
	ManifestSHA  string            `json:"manifest_sha256"`
	SyncErrorMs  float64           `json:"sync_error_ms"`
	RecoveryRate float64           `json:"recovery_rate"`
	Loss         media.LossReport  `json:"loss_report"`
	P95LatencyMs float64           `json:"p95_latency_ms"`
	StageSamples []StageSample     `json:"stage_samples"`
	Resumed      bool              `json:"resumed_from_checkpoint"`
}

// P95 computes the 95th percentile latency in milliseconds using the
// nearest-rank method; an empty sample set returns 0.
func P95(samples []StageSample) float64 {
	if len(samples) == 0 {
		return 0
	}
	ns := make([]int64, len(samples))
	for i, s := range samples {
		ns[i] = s.Nanos
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i] < ns[j] })
	rank := (95*len(ns) + 99) / 100 // ceil(0.95*n), nearest rank, 1 based
	if rank < 1 {
		rank = 1
	}
	return media.NanosToMs(ns[rank-1])
}

// RunProgram executes every rendition against one workspace and performs the
// final merge stage. It is safe to call repeatedly: recovery reuses durable
// checkpoints and idempotent artifacts, converging on an identical manifest.
func (ws *Workspace) RunProgram(opts RunOptions) (*ProgramResult, error) {
	cfg := opts.Fx.Config

	// Audio ingest happens once for the whole program on the shared clock.
	receiver := ingest.NewRTPReceiver(ingest.DefaultRTPConfig())
	for _, p := range opts.Fx.Delivered {
		if err := receiver.Receive(p); err != nil {
			return nil, err
		}
	}
	audio, loss, rtpErr := receiver.Finalize()

	var allEntries []SegmentEntry
	out := &ProgramResult{
		CaseName:     opts.CaseName,
		Seed:         opts.Seed,
		Loss:         loss,
		RecoveryRate: recoveryRateOrOne(loss),
	}
	var worstSyncNs int64
	for _, rd := range opts.Fx.Renditions {
		res, err := ws.RunTask(opts, rd, audio, loss)
		if err != nil {
			return nil, err
		}
		if res.Checkpoint != nil {
			out.Resumed = true
		}
		out.StageSamples = append(out.StageSamples, res.Samples...)
		if res.Sync.MaxAbsErrorNs > worstSyncNs {
			worstSyncNs = res.Sync.MaxAbsErrorNs
		}
		rep := RenditionReport{
			TaskKey:       res.TaskKey,
			Rendition:     res.Rendition,
			SegmentCount:  len(res.Records),
			FrameCounters: res.Frames.Counters,
			SyncErrorMs:   res.Sync.SyncErrorMs(),
			SampleCount:   res.Sync.SampleCount,
			Records:       res.Records,
		}
		entries := make([]SegmentEntry, 0, len(res.Records))
		for _, rec := range res.Records {
			entries = append(entries, SegmentEntry{
				TaskKey: res.TaskKey, Rendition: res.Rendition,
				Name: rec.Name, StartTick: rec.StartTick,
				Size: rec.Size, SHA256: rec.SHA256,
			})
		}
		delta := cfg.Renditions[0].FrameDurationTick * FramesPerSegment
		rep.Continuous = Continuity(entries, delta) == nil
		out.Renditions = append(out.Renditions, rep)
		allEntries = append(allEntries, entries...)
	}

	// Simulate a process exit after every rendition's fragments are durable
	// but before the merge stage and the committed state transition.
	if opts.CrashAfterWrite {
		return out, ErrSimulatedCrash
	}

	// Stage merge: deterministic manifest written atomically.
	manifest, err := MergeManifest(opts.CaseName, opts.Seed, allEntries)
	if err != nil {
		return nil, err
	}
	manifestBytes, err := manifest.Marshal()
	if err != nil {
		return nil, err
	}
	manifestPath, err := ws.SafeJoin("manifest.json")
	if err != nil {
		return nil, err
	}
	if err := ws.atomicWrite(manifestPath, manifestBytes); err != nil {
		return nil, err
	}
	out.ManifestSHA, _ = manifest.ManifestSHA256()
	out.SyncErrorMs = media.NanosToMs(worstSyncNs)
	out.P95LatencyMs = P95(out.StageSamples)

	// Tasks are committed only after the merge artifact is durable.
	for _, rd := range opts.Fx.Renditions {
		st, ok, err := ws.LoadState(rd.TaskKey)
		if err != nil || !ok {
			return nil, err
		}
		st.Status, st.Stage = StateCommitted, StageMerge
		if err := ws.SaveState(st); err != nil {
			return nil, err
		}
		if _, err := ws.WriteCheckpoint(rd.TaskKey, StageMerge,
			mustDigest(st.InputSummary),
			map[string]interface{}{"manifest_sha256": out.ManifestSHA}); err != nil {
			return nil, err
		}
	}
	return out, rtpErr
}

func recoveryRateOrOne(loss media.LossReport) float64 {
	if loss.Injected == 0 {
		return 1
	}
	return loss.RecoveryRate
}

func mustDigest(s InputSummary) string {
	d, err := InputDigest(s)
	if err != nil {
		return ""
	}
	return d
}

// WriteReport writes the JSON program report under the workspace root.
func (ws *Workspace) WriteReport(result *ProgramResult) (string, error) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	path, err := ws.SafeJoin("report.json")
	if err != nil {
		return "", err
	}
	if err := ws.atomicWrite(path, data); err != nil {
		return "", err
	}
	return path, nil
}
