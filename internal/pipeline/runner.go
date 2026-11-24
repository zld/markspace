package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"archivist/internal/fault"
	"archivist/internal/index"
	"archivist/internal/repair"
	"archivist/internal/scan"
)

// ErrInterrupted signals a deliberate stop requested via
// Options.InterruptAfter. It is not a failure: the checkpoint stays resumable.
type ErrInterrupted struct{ After int }

func (e *ErrInterrupted) Error() string {
	return fmt.Sprintf("interrupted deliberately after %d stage(s)", e.After)
}

// Options configure one pipeline run.
type Options struct {
	// QueueCapacity bounds how many inputs may have work in flight. It is the
	// only unbounded-memory risk in the design, so it is required.
	QueueCapacity int
	// WorkDir holds outputs, indexes, provenance and checkpoints.
	WorkDir string
	// ChunkSamples groups samples into chunks in the rebuilt file.
	ChunkSamples int
	// MovieTimescale overrides the source movie timescale.
	MovieTimescale uint32
	// RepairLimit caps a repairable backward jump in ticks; zero derives it
	// from the track timescale.
	RepairLimit int64
	// InterruptAfter aborts deliberately after N stages, used by the demo to
	// exercise resume. Zero disables it.
	InterruptAfter int
}

// InputResult is the terminal state of one input.
type InputResult struct {
	Name        string `json:"name"`
	InputPath   string `json:"input_path"`
	InputSize   int64  `json:"input_size"`
	InputSHA256 string `json:"input_sha256"`
	Status      string `json:"status"`
	// ExitCode is what a single-input invocation would return: 0 success,
	// 2 corrupt or unreparable input, 1 any other failure.
	ExitCode    int    `json:"exit_code"`
	Error       string `json:"error,omitempty"`
	ErrorOffset int64  `json:"error_offset"`
	TrackID     uint32 `json:"track_id"`
	// RepairedPTS lists a bounded sample of changed presentation timestamps,
	// each with its absolute original and repaired values. The full list is
	// always in the provenance artifact.
	RepairedPTS      []RepairedPTS `json:"repaired_pts"`
	RepairedEntries  int           `json:"repaired_entries"`
	IndexEntries     int           `json:"index_entries"`
	KeyframeEntries  int           `json:"keyframe_entries"`
	ResumeMs         int64         `json:"resume_ms"`
	ElapsedMs        int64         `json:"elapsed_ms"`
	ResumedStages    []Stage       `json:"resumed_stages,omitempty"`
	ExecutedStages   []Stage       `json:"executed_stages,omitempty"`
	OutputSHA256     string        `json:"output_sha256,omitempty"`
	OutputPath       string        `json:"output_path,omitempty"`
	ProvenanceSHA256 string        `json:"provenance_sha256,omitempty"`
	ProvenancePath   string        `json:"provenance_path,omitempty"`
	IndexSHA256      string        `json:"index_sha256,omitempty"`
	IndexPath        string        `json:"index_path,omitempty"`
	CheckpointPath   string        `json:"checkpoint_path,omitempty"`
	CheckpointStage  Stage         `json:"checkpoint_stage,omitempty"`
	CheckpointKept   bool          `json:"checkpoint_kept"`
}

// RepairedPTS is one changed timestamp: track, sample, stored and repaired
// value, and the reason it changed.
type RepairedPTS struct {
	TrackID  uint32 `json:"track_id"`
	Sample   int    `json:"sample"`
	Original int64  `json:"original"`
	Repaired int64  `json:"repaired"`
	Delta    int64  `json:"delta"`
	Kind     string `json:"kind"`
}

// leaderRepairs caps how many individual corrections are echoed into the
// per-run report. The full list always lives in the provenance artifact.
const leaderRepairs = 8

// Runner executes the pipeline. It is safe to reuse across Run calls.
type Runner struct {
	opts Options
	now  func() time.Time
	// inFlight and maxInFlight observe concurrency so tests can prove the
	// queue capacity actually bounds simultaneous work.
	inFlight    int32
	maxInFlight int32
}

// New validates opts and returns a Runner.
func New(opts Options) (*Runner, error) {
	if opts.QueueCapacity <= 0 {
		return nil, fault.New(fault.KindInvariant, fault.StagePipeline,
			"queue capacity must be positive, got %d", opts.QueueCapacity)
	}
	if opts.WorkDir == "" {
		return nil, fault.New(fault.KindInvariant, fault.StagePipeline, "work directory is required")
	}
	if opts.ChunkSamples <= 0 {
		opts.ChunkSamples = 1
	}
	return &Runner{opts: opts, now: time.Now}, nil
}

// params returns the parameter set hashed into checkpoints.
func (r *Runner) params() Params {
	return Params{
		QueueCapacity:  r.opts.QueueCapacity,
		ChunkSamples:   r.opts.ChunkSamples,
		MovieTimescale: r.opts.MovieTimescale,
		RepairLimit:    r.opts.RepairLimit,
		RulesVersion:   RulesVersion(),
	}
}

// job is one input moving through the queue.
type job struct {
	index     int
	path      string
	name      string
	started   time.Time
	interrupt bool
}

// Run processes every input, returning one result per input in input order.
// Inputs that fail do not stop the remaining inputs; the returned error is
// the first failure so the CLI can map it to an exit code.
func (r *Runner) Run(ctx context.Context, inputs []string) ([]*InputResult, error) {
	if len(inputs) == 0 {
		return nil, fault.New(fault.KindInvariant, fault.StagePipeline, "no inputs given")
	}
	if err := os.MkdirAll(r.opts.WorkDir, 0o755); err != nil {
		return nil, fault.New(fault.KindIO, fault.StagePipeline, "create work dir").In(r.opts.WorkDir).Wrap(err)
	}
	// A buffered channel of exactly QueueCapacity limits how many inputs can
	// be in a stage at once: producers block when it is full, so memory and
	// open files stay bounded regardless of how many inputs are supplied.
	queue := make(chan *job, r.opts.QueueCapacity)
	results := make([]*InputResult, len(inputs))
	failCh := make(chan error, 1)
	var stopMu sync.Mutex
	var firstStop *ErrInterrupted
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	// Consumer count equals queue capacity: that is the maximum number of
	// concurrent inputs, which is what the parameter promises.
	for i := 0; i < r.opts.QueueCapacity; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				r.enterInput()
				res, err := r.process(cctx, j)
				r.leaveInput()
				results[j.index] = res
				if err != nil {
					var stop *ErrInterrupted
					if errors.As(err, &stop) {
						// Deliberate interruption: keep going with the other
						// inputs; it is not a run failure, but the caller is
						// told that the run stopped short.
						stopMu.Lock()
						if firstStop == nil {
							firstStop = stop
						}
						stopMu.Unlock()
						continue
					}
					f, isFault := fault.AsFault(err)
					// Report the first real failure. Cancellation errors are
					// side effects of another failure, not independent faults.
					if !isFault || f.Kind != fault.KindCancel {
						select {
						case failCh <- err:
						default:
						}
					}
					// Corrupt input is an expected per-input outcome: the
					// remaining inputs still get their own diagnosis, so one
					// bad file does not hide another. Any other failure (IO,
					// invariant, queue) cancels upstream work instead of
					// letting the producer fill the queue for a doomed run.
					if !isFault || (f.Kind != fault.KindCorrupt && f.Kind != fault.KindTimestamp) {
						cancel()
					}
				}
			}
		}()
	}

producer:
	for idx, in := range inputs {
		j := &job{index: idx, path: in, name: inputName(in), started: r.now()}
		select {
		case queue <- j:
		case <-cctx.Done():
			break producer
		}
		if cctx.Err() != nil {
			break producer
		}
	}
	close(queue)
	wg.Wait()

	// Inputs that never started are reported as cancelled rather than being
	// silently dropped.
	for i, res := range results {
		if res != nil {
			continue
		}
		results[i] = &InputResult{
			Name:        inputName(inputs[i]),
			InputPath:   inputs[i],
			Status:      "cancelled",
			Error:       "not started: an earlier input failed",
			ErrorOffset: -1,
		}
	}

	var firstErr error
	select {
	case firstErr = <-failCh:
	default:
	}
	if firstErr == nil {
		// A typed nil would read as a non-nil error interface, so only
		// return the interruption when one actually occurred.
		if firstStop != nil {
			return results, firstStop
		}
		return results, nil
	}
	for _, res := range results {
		if res == nil || res.Status != "error" {
			continue
		}
		if firstErr == nil {
			firstErr = fault.New(fault.KindIO, fault.StagePipeline, "%s", res.Error)
		}
		break
	}
	return results, firstErr
}

// enterInput records that one more input is being processed and updates the
// high-water mark.
func (r *Runner) enterInput() {
	n := atomic.AddInt32(&r.inFlight, 1)
	for {
		max := atomic.LoadInt32(&r.maxInFlight)
		if n <= max || atomic.CompareAndSwapInt32(&r.maxInFlight, max, n) {
			return
		}
	}
}

func (r *Runner) leaveInput() { atomic.AddInt32(&r.inFlight, -1) }

// MaxInFlight reports the highest number of inputs processed simultaneously.
func (r *Runner) MaxInFlight() int { return int(atomic.LoadInt32(&r.maxInFlight)) }

func inputName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// stageRecord is the in-memory state carried between stages.
type stageRecord struct {
	checkpoint *Checkpoint
	container  *scan.Container
	repaired   *repair.Result
	ix         *index.Index
	res        *InputResult
	executed   int
}

// process runs one input through the pipeline, resuming from its checkpoint.
func (r *Runner) process(ctx context.Context, j *job) (*InputResult, error) {
	res := &InputResult{
		Name:        j.name,
		InputPath:   j.path,
		Status:      "ok",
		ErrorOffset: -1,
	}
	started := r.now()
	defer func() { res.ElapsedMs = r.now().Sub(started).Milliseconds() }()

	inSHA, inSize, err := hashFile(j.path)
	if err != nil {
		return r.fail(res, err)
	}
	res.InputSHA256 = inSHA
	res.InputSize = inSize
	params := r.params()
	cpPath := checkpointPath(filepath.Join(r.opts.WorkDir, "checkpoints"), j.name, inSHA, params.Hash())
	res.CheckpointPath = cpPath

	cp, err := loadCheckpoint(cpPath)
	if err != nil {
		return r.fail(res, err)
	}
	if cp != nil && !cp.ValidateAgainst(inSHA, params) {
		// Different input bytes or parameters: the old checkpoint is not a
		// valid resume point and must not influence this run.
		cp = nil
	}
	if cp == nil {
		cp = &Checkpoint{
			Version:     CheckpointVersion,
			InputName:   j.name,
			InputPath:   j.path,
			InputSize:   inSize,
			InputSHA256: inSHA,
			Params:      params,
			ParamsHash:  params.Hash(),
			StageVers:   map[Stage]string{},
			Status:      "running",
		}
		for s, v := range stageVersion {
			cp.StageVers[s] = v
		}
	}
	res.ResumedStages = append([]Stage(nil), cp.Completed...)
	resumeStart := r.now()

	// Interrupting means "stop after interrupting the first execution", so
	// only an otherwise-fresh run is eligible.
	if r.opts.InterruptAfter > 0 && !cp.Has(StageProvenance) {
		j.interrupt = true
	}

	rec := &stageRecord{checkpoint: cp, res: res}
	if err := r.runStages(ctx, j, rec, inSHA, cpPath); err != nil {
		var stop *ErrInterrupted
		if errors.As(err, &stop) {
			// A deliberate interruption is not a failure: the checkpoint is
			// intact and the next run continues from it.
			res.Status = "interrupted"
			res.ExitCode = 1
			res.Error = err.Error()
			res.CheckpointStage = cp.LastStage()
			res.CheckpointKept = true
			return res, err
		}
		// A corrupt input keeps its checkpoint too, so a repaired input can
		// resume from the stage that failed.
		out, ferr := r.fail(res, err)
		out.CheckpointStage = cp.LastStage()
		return out, ferr
	}
	res.ResumeMs = r.now().Sub(resumeStart).Milliseconds()
	res.Status = "ok"
	res.ExitCode = 0
	res.CheckpointStage = cp.LastStage()
	res.RepairedPTS = summarizeRepairs(cp.Repair)
	res.RepairedEntries = cp.RepairedEntries
	res.IndexEntries = cp.IndexEntries
	res.KeyframeEntries = cp.KeyframeEntries
	res.OutputSHA256 = cp.Output.SHA256
	res.OutputPath = cp.Output.Path
	res.ProvenanceSHA256 = cp.ProvenanceDigest
	res.ProvenancePath = cp.Provenance.Path
	res.IndexSHA256 = cp.Index.SHA256
	res.IndexPath = cp.Index.Path
	res.CheckpointKept = true
	return res, nil
}

// runStages executes each stage in order, skipping any that completed with an
// artifact that still verifies. It is the single place resume semantics live.
func (r *Runner) runStages(ctx context.Context, j *job, rec *stageRecord, inSHA, cpPath string) error {
	// Persist the initial progress record before any work so an input that
	// fails during the very first stage still leaves a resumable checkpoint
	// identifying its input and parameters.
	if err := rec.checkpoint.save(cpPath); err != nil {
		return err
	}
	for _, stage := range stageOrder {
		if err := ctx.Err(); err != nil {
			return fault.New(fault.KindCancel, fault.StagePipeline, "run cancelled before %s", stage).Wrap(err)
		}
		if rec.checkpoint.Has(stage) {
			reusable, err := r.reuse(stage, rec, inSHA)
			if err != nil {
				return err
			}
			if reusable {
				continue
			}
			// The recorded artifact no longer matches; every later stage
			// depended on it, so truncate and rebuild from here.
			rec.checkpoint.TruncateAfter(prevStage(stage))
			_ = rec.checkpoint.save(cpPath)
		}
		if j.interrupt && r.opts.InterruptAfter > 0 && rec.executed >= r.opts.InterruptAfter {
			return &ErrInterrupted{After: rec.executed}
		}
		if err := r.execute(ctx, stage, rec, inSHA, cpPath); err != nil {
			return err
		}
		rec.executed++
		rec.res.ExecutedStages = append(rec.res.ExecutedStages, stage)
		rec.checkpoint.MarkComplete(stage)
		rec.checkpoint.Status = "running"
		rec.checkpoint.StageVers[stage] = stageVersion[stage]
		if err := rec.checkpoint.save(cpPath); err != nil {
			return err
		}
		if j.interrupt && r.opts.InterruptAfter > 0 && rec.executed >= r.opts.InterruptAfter {
			return &ErrInterrupted{After: rec.executed}
		}
	}
	rec.checkpoint.Status = "ok"
	return rec.checkpoint.save(cpPath)
}

func prevStage(s Stage) Stage {
	rank := stageRank(s)
	if rank == 0 {
		return ""
	}
	return stageOrder[rank-1]
}

// reuse checks whether a previously completed stage can be trusted.
func (r *Runner) reuse(stage Stage, rec *stageRecord, inSHA string) (bool, error) {
	cp := rec.checkpoint
	switch stage {
	case StageScan:
		return artifactVerifies(cp.Scan), nil
	case StageRepair:
		return artifactVerifies(cp.Repair), nil
	case StageWrite:
		return artifactVerifies(cp.Output), nil
	case StageIndex:
		return artifactVerifies(cp.Index), nil
	case StageProvenance:
		return artifactVerifies(cp.Provenance), nil
	case StageVerify:
		// Verification is only meaningful if what it verified still matches.
		return artifactVerifies(cp.Output) && artifactVerifies(cp.Index), nil
	}
	return false, nil
}

// artifactVerifies reports whether a recorded artifact still exists with the
// exact size and SHA-256 recorded for it. Resume never trusts a path alone: a
// truncated, edited or missing artifact forces its stage and every later one
// to rerun. An I/O error during hashing is treated as "not reusable" so the
// rebuild path produces the authoritative error.
func artifactVerifies(a Artifact) bool {
	if !artifactExists(a) {
		return false
	}
	if a.SHA256 == "" {
		return false
	}
	return verifyArtifact(a) == nil
}

func artifactExists(a Artifact) bool {
	if a.Path == "" || a.SHA256 == "" {
		return false
	}
	st, err := os.Stat(a.Path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Size() == a.Size
}

// execute performs one stage.
func (r *Runner) execute(ctx context.Context, stage Stage, rec *stageRecord, inSHA, cpPath string) error {
	cp := rec.checkpoint
	res := rec.res
	switch stage {
	case StageScan:
		reader, err := scan.Open(res.InputPath)
		if err != nil {
			return err
		}
		defer reader.Close()
		c, err := reader.Scan()
		if err != nil {
			return err
		}
		art, err := writeScanArtifact(c, artifactPath(r.stageDir(), res.Name, cp.ParamsHash, ".scan.json"))
		if err != nil {
			return err
		}
		cp.Scan = art
		rec.container = c
		return nil

	case StageRepair:
		c := rec.container
		if c == nil {
			loaded, err := loadScanArtifact(cp.Scan)
			if err != nil {
				return err
			}
			c = loaded
			rec.container = c
		}
		policy := repair.DefaultPolicy(c.Tracks[0].Timescale)
		if r.opts.RepairLimit > 0 {
			policy.MaxBackwardTicks = r.opts.RepairLimit
		}
		rep, err := repair.Repair(c, policy)
		if err != nil {
			return err
		}
		if err := repair.VerifyMonotonic(rep); err != nil {
			return err
		}
		art, err := writeRepairArtifact(rep, artifactPath(r.stageDir(), res.Name, cp.ParamsHash, ".repair.json"))
		if err != nil {
			return err
		}
		cp.Repair = art
		rec.repaired = rep
		cp.RepairedEntries = rep.TotalRepaired()
		return nil

	case StageWrite:
		c, rep, err := r.ensure(rec)
		if err != nil {
			return err
		}
		outPath := filepath.Join(r.opts.WorkDir, "output", res.Name+".mp4")
		ix, err := index.Rebuild(c, rep, outPath, index.RebuildOptions{
			MovieTimescale: r.opts.MovieTimescale,
			ChunkSamples:   r.opts.ChunkSamples,
		})
		if err != nil {
			return err
		}
		rec.ix = ix
		sum, size, err := hashFile(outPath)
		if err != nil {
			return err
		}
		cp.Output = Artifact{Path: outPath, Size: size, SHA256: sum}
		cp.IndexEntries = ix.EntryCount()
		cp.KeyframeEntries = ix.KeyframeCount()
		return nil

	case StageIndex:
		if rec.ix == nil {
			// A resumed run skipped the write stage, so derive the index from
			// the output artifact and re-verified repair timeline.
			_, rep, err := r.ensure(rec)
			if err != nil {
				return err
			}
			ix, err := index.FromOutput(cp.Output.Path, rep)
			if err != nil {
				return err
			}
			rec.ix = ix
		}
		ixPath := filepath.Join(r.opts.WorkDir, "index", res.Name+".json")
		art, err := WriteIndex(rec.ix, ixPath)
		if err != nil {
			return err
		}
		cp.Index = art
		return nil

	case StageVerify:
		// Re-verify everything a resumed run has to trust: output bytes,
		// index bytes, and the repaired timeline's internal consistency.
		if err := verifyArtifact(cp.Output); err != nil {
			return err
		}
		if err := verifyArtifact(cp.Index); err != nil {
			return err
		}
		_, rep, err := r.ensure(rec)
		if err != nil {
			return err
		}
		if err := repair.VerifyMonotonic(rep); err != nil {
			return err
		}
		if err := verifyIndexAgainstScan(cp.Output.Path, cp, rep); err != nil {
			return err
		}
		return nil

	case StageProvenance:
		c, rep, err := r.ensure(rec)
		if err != nil {
			return err
		}
		if rec.ix == nil {
			ix, err := index.Build(rep)
			if err != nil {
				return err
			}
			// Offsets in the output file are already recorded in the index
			// artifact; provenance only needs counts and bases.
			rec.ix = ix
		}
		input := Artifact{Path: res.InputPath, Size: cp.InputSize, SHA256: inSHA}
		prov, err := BuildProvenance(input, cp.Params, RulesVersion(), rep, rec.ix, cp.Output, cp.LastStage(), summarizeSkipped(c))
		if err != nil {
			return err
		}
		provPath := filepath.Join(r.opts.WorkDir, "provenance", res.Name+".json")
		art, err := WriteProvenance(prov, provPath)
		if err != nil {
			return err
		}
		cp.Provenance = art
		cp.ProvenanceDigest = prov.Digest
		return nil
	}
	return fault.New(fault.KindInvariant, fault.StagePipeline, "unknown stage %q", stage)
}

// ensure returns a container and repair result, recomputing them if a resume
// skipped the in-memory stages.
func (r *Runner) ensure(rec *stageRecord) (*scan.Container, *repair.Result, error) {
	if rec.container == nil {
		c, err := loadScanArtifact(rec.checkpoint.Scan)
		if err != nil {
			return nil, nil, err
		}
		rec.container = c
	}
	if rec.repaired == nil {
		rep, err := loadRepairArtifact(rec.checkpoint.Repair)
		if err != nil {
			return nil, nil, err
		}
		rec.repaired = rep
	}
	return rec.container, rec.repaired, nil
}

// stageDir holds the durable scan and repair artifacts.
func (r *Runner) stageDir() string {
	return filepath.Join(r.opts.WorkDir, "stages")
}

func (r *Runner) rescan(path string) (*scan.Container, error) {
	reader, err := scan.Open(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return reader.Scan()
}

// fail records an error on the result and preserves a resumable checkpoint.
func (r *Runner) fail(res *InputResult, err error) (*InputResult, error) {
	res.Status = "error"
	res.ExitCode = fault.ExitCode(err)
	res.Error = err.Error()
	res.ErrorOffset = -1
	if f, ok := fault.AsFault(err); ok {
		res.ErrorOffset = f.Offset
		res.TrackID = f.TrackID
	}
	res.CheckpointKept = true
	return res, err
}

// verifyIndexAgainstScan confirms the written index describes the rebuilt
// file: same tracks, same sample counts, same absolute byte offsets. It is the
// check that makes a resumed verification as strong as a fresh one.
func verifyIndexAgainstScan(path string, cp *Checkpoint, rep *repair.Result) error {
	c, err := (&Runner{}).rescan(path)
	if err != nil {
		return err
	}
	byID := make(map[uint32]*scan.Track, len(c.Tracks))
	for _, t := range c.Tracks {
		byID[t.ID] = t
	}
	for _, tr := range rep.Tracks {
		st, ok := byID[tr.TrackID]
		if !ok {
			return fault.New(fault.KindInvariant, fault.StageVerify,
				"rebuilt file lost track %d", tr.TrackID).In(path)
		}
		if len(st.Samples) != len(tr.Entries) {
			return fault.New(fault.KindInvariant, fault.StageVerify,
				"rebuilt track %d has %d samples, repair produced %d",
				tr.TrackID, len(st.Samples), len(tr.Entries)).In(path)
		}
	}
	ix, err := index.Load(cp.Index.Path)
	if err != nil {
		return err
	}
	for _, tr := range rep.Tracks {
		it, ok := ix.Track(tr.TrackID)
		if !ok {
			return fault.New(fault.KindInvariant, fault.StageVerify,
				"index is missing track %d", tr.TrackID).In(cp.Index.Path)
		}
		st := byID[tr.TrackID]
		if len(it.Entries) != len(st.Samples) {
			return fault.New(fault.KindInvariant, fault.StageVerify,
				"index track %d has %d entries, file has %d samples",
				tr.TrackID, len(it.Entries), len(st.Samples)).In(cp.Index.Path)
		}
		for i, e := range it.Entries {
			if e.Offset != st.Samples[i].Offset || e.Size != st.Samples[i].Size {
				return fault.New(fault.KindInvariant, fault.StageVerify,
					"index track %d sample %d points at %d/%d, file has %d/%d",
					tr.TrackID, i+1, e.Offset, e.Size,
					st.Samples[i].Offset, st.Samples[i].Size).In(cp.Index.Path)
			}
		}
	}
	return nil
}

// summarizeRepairs reads the persisted repair artifact and returns a bounded
// leading sample of the corrections. A missing artifact (for example a run
// that failed before repair) yields no entries rather than an error.
func summarizeRepairs(a Artifact) []RepairedPTS {
	if !artifactExists(a) {
		return nil
	}
	rep, err := loadRepairArtifact(a)
	if err != nil {
		return nil
	}
	var out []RepairedPTS
	for _, tr := range rep.Tracks {
		for _, e := range tr.Entries {
			if e.Kind == repair.KindUnchanged {
				continue
			}
			out = append(out, RepairedPTS{
				TrackID: tr.TrackID, Sample: e.Sample,
				Original: e.Original, Repaired: e.Repaired,
				Delta: e.Delta, Kind: e.Kind,
			})
			if len(out) >= leaderRepairs {
				return out
			}
		}
	}
	return out
}
