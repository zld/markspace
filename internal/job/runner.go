package job

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"medpipe/internal/apperr"
	"medpipe/internal/graph"
	"medpipe/internal/manifest"
	"medpipe/internal/report"
	"medpipe/internal/store"
)

// Options controls one run or resume.
type Options struct {
	Root        string
	JobID       string
	BudgetUnits int
	Fault       string
}

type exec struct {
	opts    Options
	m       *manifest.Manifest
	hash    string
	baseDir string
	jobKey  string
	state   *State
	stateMu sync.Mutex
}

// Run loads a manifest and executes or reuses the job.
func Run(opts Options, manifestPath string) (*report.Summary, error) {
	if opts.Root == "" {
		opts.Root = ".tmp/run"
	}
	if opts.JobID == "" {
		opts.JobID = "default"
	}
	m, hash, err := manifest.Load(manifestPath)
	if err != nil {
		return nil, err
	}
	absManifest, err := filepath.Abs(manifestPath)
	if err != nil {
		return nil, err
	}
	return execute(opts, m, hash, filepath.Dir(absManifest), absManifest)
}

// Resume loads the manifest path recorded in durable state and continues.
func Resume(opts Options) (*report.Summary, error) {
	if opts.Root == "" || opts.JobID == "" {
		return nil, apperr.New("E_JOB_USAGE", "resume requires --root and --job-id").WithStage("state")
	}
	pre, ok, err := Load(opts.Root, opts.JobID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.New("E_JOB_NOT_FOUND", "no job state for job %q", opts.JobID).
			WithStage("state").WithRecoverable(false)
	}
	m, hash, err := manifest.Load(pre.ManifestPath)
	if err != nil {
		return nil, err
	}
	return execute(opts, m, hash, filepath.Dir(pre.ManifestPath), pre.ManifestPath)
}

func execute(opts Options, m *manifest.Manifest, hash, baseDir, manifestPath string) (*report.Summary, error) {
	sourceHashes := make(map[string]string, len(m.Tracks))
	for _, t := range m.Tracks {
		abs := filepath.Join(baseDir, filepath.FromSlash(t.Path))
		sha, _, err := store.HashFile(abs)
		if err != nil {
			return nil, apperr.New("E_TRACK_FILE_MISSING", "hash source %s: %v", t.Path, err).
				WithStage("manifest").WithTrack(t.ID, t.Path)
		}
		sourceHashes[t.Path] = sha
	}
	// Budget is an admission policy, not a content-processing parameter, so an
	// operator can raise a paused budget without changing the identity of the
	// reusable manifest/source computation.
	keyParams := m.Parameters
	keyParams.BudgetUnits = 0
	jobKey := manifest.JobKey(hash, sourceHashes, keyParams)

	lock, err := AcquireKeyLock(opts.Root, jobKey)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()

	state, existed, err := Load(opts.Root, opts.JobID)
	if err != nil {
		return nil, err
	}
	if existed && state.JobKey != "" && state.JobKey != jobKey {
		return nil, apperr.New("E_JOB_KEY_MISMATCH", "job id %q already belongs to a different manifest/source/parameters", opts.JobID).
			WithStage("state").WithRecoverable(false)
	}
	state.JobID = opts.JobID
	state.JobKey = jobKey
	state.ManifestPath = manifestPath
	state.ManifestHash = hash
	state.Status = StatusRunning
	if opts.Fault != "" {
		state.Fault = opts.Fault
	}
	if err := state.Save(opts.Root, opts.JobID); err != nil {
		return nil, err
	}

	x := &exec{opts: opts, m: m, hash: hash, baseDir: baseDir, jobKey: jobKey, state: state}
	results, err := x.processTracks()
	if err != nil {
		return nil, err
	}

	samples, repairs := 0, 0
	isolatedIDs := make([]string, 0)
	conclusions := make([]report.TrackConclusion, 0, len(results))
	for _, r := range results {
		samples += r.Scan.SampleCount
		repairs += r.Conclusion.RepairCount
		if r.Conclusion.Isolation != nil {
			isolatedIDs = append(isolatedIDs, r.Conclusion.ID)
		}
		conclusions = append(conclusions, r.Conclusion)
	}
	estimate := samples * 4
	actual := estimate + repairs*3
	state.Budget = BudgetState{
		Samples: samples, Repairs: repairs, EstimateUnits: estimate,
		ActualUnits: actual, BudgetUnits: opts.BudgetUnits,
	}
	if opts.BudgetUnits > 0 && actual > opts.BudgetUnits {
		state.Budget.Paused = true
		state.Status = StatusPaused
		_ = state.Save(opts.Root, opts.JobID)
		return nil, apperr.New("E_BUDGET_PAUSED",
			"actual units %d exceed budget %d; committed track phases retained", actual, opts.BudgetUnits).
			WithStage("budget").WithRecoverable(true)
	}

	nodes := make([]graph.Node, 0, len(m.Tracks)+len(m.Dependencies))
	for _, t := range m.Tracks {
		nodes = append(nodes, graph.Node{ID: t.ID})
	}
	for _, d := range m.Dependencies {
		nodes = append(nodes, graph.Node{ID: d.Stage, Requires: d.Requires})
	}
	plan, err := graph.Build(nodes)
	if err != nil {
		return nil, err
	}
	skipSet := plan.SkipClosure(isolatedIDs...)
	skipped := make([]string, 0)
	for _, d := range m.Dependencies {
		if skipSet[d.Stage] {
			skipped = append(skipped, d.Stage)
		}
	}

	pub := newPublisher(opts.Root, opts.JobID, state)
	timelineSHA, err := pub.Publish("artifact:timeline", timelineName, func() ([]byte, error) {
		return buildTimeline(hash, results)
	}, opts.Fault)
	if err != nil {
		return nil, err
	}
	indexSHA, err := pub.Publish("artifact:index", indexName, func() ([]byte, error) {
		return buildIndex(results)
	}, "")
	if err != nil {
		return nil, err
	}
	auditSHA, err := pub.Publish("artifact:audit", auditName, func() ([]byte, error) {
		return buildAudit(results)
	}, "")
	if err != nil {
		return nil, err
	}

	status := "ok"
	if len(isolatedIDs) > 0 {
		status = "isolated"
	}
	built := report.Build(status, hash, timelineSHA, jobKey, opts.BudgetUnits, conclusions, skipped)
	summary := &built
	if len(isolatedIDs) > 0 {
		summary.ErrorCode = "E_TRACK_ISOLATED"
		summary.ExitCode = apperr.ExitIsolated
	}
	summary.ArtifactHashes = map[string]string{
		timelineName: timelineSHA,
		indexName:    indexSHA,
		auditName:    auditSHA,
	}
	_, err = pub.Publish("artifact:summary", summaryName, func() ([]byte, error) {
		summary.WithEmitted(pub.Emitted() + 1)
		return json.MarshalIndent(summary, "", "  ")
	}, "")
	if err != nil {
		return nil, err
	}
	summary.WithEmitted(pub.Emitted())

	state.Status = StatusCompleted
	if len(isolatedIDs) > 0 {
		state.Status = StatusIsolated
	}
	_ = state.Save(opts.Root, opts.JobID)

	if len(isolatedIDs) > 0 {
		return summary, apperr.New("E_TRACK_ISOLATED", "%d track(s) isolated; skipped stages=%v", len(isolatedIDs), skipped).
			WithStage("runner").WithRecoverable(false)
	}
	return summary, nil
}

func (x *exec) processTracks() ([]*TrackResult, error) {
	results := make([]*TrackResult, len(x.m.Tracks))
	errs := make([]error, len(x.m.Tracks))
	var wg sync.WaitGroup
	for i, track := range x.m.Tracks {
		wg.Add(1)
		go func(i int, track manifest.Track) {
			defer wg.Done()
			results[i], errs[i] = x.loadOrProcessTrack(i, track)
		}(i, track)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (x *exec) loadOrProcessTrack(i int, track manifest.Track) (*TrackResult, error) {
	stageName := "track:" + track.ID
	cpPath := filepath.Join(Dir(x.opts.Root, x.opts.JobID), "checkpoints", fmt.Sprintf("track-%d.json", i))

	x.stateMu.Lock()
	st := x.state.Stage(stageName)
	committed := st.Status == StageCommitted
	wantSHA := st.SHA256
	wantSize := st.Size
	x.stateMu.Unlock()

	if committed {
		if data, err := os.ReadFile(cpPath); err == nil {
			valid := true
			if wantSHA != "" {
				if _, _, verr := store.Verify(cpPath, wantSHA, wantSize); verr != nil {
					valid = false
				}
			}
			if valid {
				var tr TrackResult
				if err := json.Unmarshal(data, &tr); err == nil {
					return &tr, nil
				}
			}
			x.stateMu.Lock()
			x.state.Stage(stageName).Status = StagePending
			x.stateMu.Unlock()
		}
	}

	tr, err := processTrack(x.m, x.baseDir, track)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(tr, "", "  ")
	if err != nil {
		return nil, apperr.New("E_JOB_STATE", "encode track checkpoint: %v", err).WithStage("state")
	}
	data = append(data, '\n')
	sha, err := store.WriteAtomic(cpPath, data)
	if err != nil {
		return nil, err
	}

	x.stateMu.Lock()
	defer x.stateMu.Unlock()
	st = x.state.Stage(stageName)
	st.Status = StageCommitted
	st.Artifact = cpPath
	st.SHA256 = sha
	st.Size = int64(len(data))
	st.FirstSample = 1
	st.LastSample = tr.Scan.SampleCount
	st.CheckpointSample = tr.Conclusion.CheckpointSample
	if err := x.state.Save(x.opts.Root, x.opts.JobID); err != nil {
		return nil, err
	}
	return tr, nil
}
