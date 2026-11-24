// Package pipeline orchestrates parse, scan, repair, index and verify for one
// demo run. Each stage is checkpointed with input, parameter and stage-version
// digests; a rerun resumes from the last trusted checkpoint and reproduces the
// same output digest.
package pipeline

import (
	"os"
	"path/filepath"

	"mediad/internal/checkpoint"
	"mediad/internal/clock"
	"mediad/internal/fixture"
	"mediad/internal/index"
	"mediad/internal/manifest"
	"mediad/internal/metrics"
	mp4 "mediad/internal/mp4"
	"mediad/internal/provenance"
)

// Options configures one run.
type Options struct {
	WorkDir   string
	Seed      int64
	TargetBps int64
	Corrupt   string
	Resume    bool
	// ExternalManifest marks a caller-supplied manifest that must never be
	// overwritten by fixture generation.
	ExternalManifest bool
	InferSync        bool
	Regenerate       bool
	ManifestPath     string
	MediaRoot        string
	CheckpointDir    string
	OutDir           string
	Policy           mp4.RepairPolicy
	Version          int
}

// Defaults fills omitted options.
func (o *Options) Defaults() {
	if o.WorkDir == "" {
		o.WorkDir = "demo-work"
	}
	if o.CheckpointDir == "" {
		o.CheckpointDir = filepath.Join(o.WorkDir, "checkpoints")
	}
	if o.OutDir == "" {
		o.OutDir = filepath.Join(o.WorkDir, "out")
	}
	if o.ManifestPath == "" {
		o.ManifestPath = filepath.Join(o.WorkDir, "manifest.json")
	}
	if o.MediaRoot == "" {
		o.MediaRoot = o.WorkDir
	}
	if o.Version == 0 {
		o.Version = 1
	}
	// Missing keyframe flags are recoverable: infer conservative GOP boundaries
	// and label the result derived so it stays auditable.
	if o.Corrupt == fixture.CorruptMissingSync {
		o.InferSync = true
	}
	if o.Policy.MaxGapTicks == 0 {
		o.Policy = mp4.DefaultPolicy()
	}
}

// paramSet is the canonical parameter snapshot hashed into every checkpoint.
type paramSet struct {
	Seed      int64  `json:"seed"`
	TargetBps int64  `json:"target_bps"`
	Corrupt   string `json:"corrupt"`
	InferSync bool   `json:"infer_sync"`
	Version   int    `json:"version"`
	IndexVer  int    `json:"index_version"`
	RepairVer int    `json:"repair_version"`
	// Policy fields affect repaired output, so they belong here.
	MaxGapTicks int64 `json:"max_gap_ticks"`
}

// Result is the deliverable summary.
type Result struct {
	Report      *metrics.Report         `json:"report"`
	Provenance  *provenance.Record      `json:"provenance"`
	Indexes     map[string]*index.Index `json:"-"`
	Resumed     bool                    `json:"resumed"`
	OutputSHA   string                  `json:"output_sha256"`
	InputSHA    string                  `json:"input_sha256"`
	ManifestSHA string                  `json:"manifest_sha256"`
}

// Run executes the pipeline. It returns a Result on success; any failure keeps
// already-written checkpoints and artifacts intact.
func Run(opts Options) (*Result, error) {
	opts.Defaults()
	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		return nil, clock.New(clock.StageArgs, clock.CodeIO, "create work dir: %v", err)
	}
	// A missing target is filled from the manifest (verify mode); a negative or
	// absurd value is still an argument error.
	if opts.TargetBps < 0 || opts.TargetBps > 2_000_000_000 {
		return nil, clock.New(clock.StageArgs, clock.CodeBadArgs, "target bitrate %d out of range", opts.TargetBps)
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, clock.New(clock.StageArgs, clock.CodeIO, "create out dir: %v", err)
	}

	// -------------------- prepare media + manifest --------------------
	// The manifest is read before the parameter digest because verify mode
	// derives the target bitrate from it; hashing must see the same parameters
	// for demo and verify so checkpoints are interchangeable.
	manifestSHA, sources, err := prepare(opts)
	if err != nil {
		return nil, err
	}
	man, err := manifest.ParseFile(opts.ManifestPath, opts.MediaRoot)
	if err != nil {
		return nil, err
	}
	man.SHA256 = manifestSHA
	if opts.TargetBps == 0 {
		opts.TargetBps = man.TargetBps
	}
	if opts.Seed == 0 && man.Seed != 0 {
		opts.Seed = man.Seed
	}

	params := paramSet{
		Seed: opts.Seed, TargetBps: opts.TargetBps, Corrupt: opts.Corrupt,
		InferSync: opts.InferSync, Version: opts.Version,
		IndexVer: index.Version, RepairVer: 1, MaxGapTicks: opts.Policy.MaxGapTicks,
	}
	paramSHA, err := checkpoint.ParamsFingerprint(params)
	if err != nil {
		return nil, err
	}

	store, err := checkpoint.Open(opts.CheckpointDir)
	if err != nil {
		return nil, err
	}

	// Per-camera input digests feed both checkpoints and provenance.
	inputSHA, err := hashSources(sources)
	if err != nil {
		return nil, err
	}

	// -------------------- plan resume --------------------
	keys := map[checkpoint.Stage]checkpoint.Key{}
	for _, st := range checkpoint.Order() {
		keys[st] = checkpoint.Key{InputSHA: inputSHA, ParamSHA: paramSHA, StageVer: opts.Version}
	}
	lastValid, runStages, notes, err := store.ResumePlan(keys)
	if err != nil {
		return nil, err
	}
	_ = lastValid
	resumed := opts.Resume && lastValid != ""
	ran := map[checkpoint.Stage]bool{}
	for _, st := range runStages {
		ran[st] = true
	}

	prov := &provenance.Record{
		Seed: opts.Seed, TargetBps: opts.TargetBps, ManifestSHA: manifestSHA,
	}
	for _, n := range notes {
		prov.Stages = append(prov.Stages, provenance.StageRecord{Stage: "resume", Status: "invalidated", Detail: n})
	}

	// -------------------- scan --------------------
	scans, scanResumed, err := loadOrScan(store, opts, man, keys[checkpoint.StageScan], ran[checkpoint.StageScan])
	if err != nil {
		return nil, err
	}
	if err := recordStage(store, prov, checkpoint.StageScan, keys[checkpoint.StageScan], scans, scanResumed, ""); err != nil {
		return nil, err
	}

	// -------------------- repair --------------------
	repairs, repairResumed, err := loadOrRepair(store, opts, man, scans, keys[checkpoint.StageRepair], ran[checkpoint.StageRepair])
	if err != nil {
		return nil, err
	}
	if err := recordStage(store, prov, checkpoint.StageRepair, keys[checkpoint.StageRepair], repairs, repairResumed, ""); err != nil {
		return nil, err
	}

	// -------------------- index --------------------
	indexes, indexResumed, err := loadOrIndex(store, opts, repairs, keys[checkpoint.StageIndex], ran[checkpoint.StageIndex])
	if err != nil {
		return nil, err
	}
	if err := recordStage(store, prov, checkpoint.StageIndex, keys[checkpoint.StageIndex], indexes, indexResumed, ""); err != nil {
		return nil, err
	}

	// -------------------- verify --------------------
	verifyRes := verify(repairs, indexes, man, opts)
	if ran[checkpoint.StageVerify] {
		if err := saveStage(store, checkpoint.StageVerify, keys[checkpoint.StageVerify],
			payload[*VerifyResult]{Schema: schemaV1, Value: verifyRes}); err != nil {
			return nil, err
		}
	}
	if err := recordStage(store, prov, checkpoint.StageVerify, keys[checkpoint.StageVerify], verifyRes, !ran[checkpoint.StageVerify], ""); err != nil {
		return nil, err
	}
	if err := verifyRes.Failure(); err != nil {
		// A failed verification must not be reported as a delivery.
		return &Result{Provenance: prov, Resumed: resumed}, err
	}

	// -------------------- metrics --------------------
	report := measure(repairs, indexes, man, opts)

	// -------------------- provenance --------------------
	// Repairs and indexes are keyed by camera; a file may carry several
	// tracks, so match each manifest track to the repaired track with the
	// same id rather than assuming ordering.
	for _, e := range man.Entries {
		rep := repairs[e.Camera]
		idx := indexes[e.Camera]
		if rep == nil || idx == nil {
			continue
		}
		byID := map[uint32]int{}
		for i, tr := range rep.Tracks {
			byID[tr.TrackID] = i
		}
		idxByID := map[uint32]int{}
		for i, tr := range idx.Tracks {
			idxByID[tr.TrackID] = i
		}
		for _, spec := range e.Tracks {
			rp, ok := byID[spec.TrackID]
			if !ok {
				continue
			}
			ip, ok := idxByID[spec.TrackID]
			if !ok {
				continue
			}
			cam, err := provenance.BuildCamera(e.Camera, e.File, e.Line, sources[e.Camera],
				&rep.Tracks[rp], &idx.Tracks[ip])
			if err != nil {
				return nil, err
			}
			prov.Cameras = append(prov.Cameras, cam)
		}
	}
	sha, err := prov.Finalize()
	if err != nil {
		return nil, err
	}
	prov.Resumed = resumed
	prov.ResumeNotes = notes
	if err := prov.Write(filepath.Join(opts.OutDir, "provenance.json")); err != nil {
		return nil, err
	}
	reportPath := filepath.Join(opts.OutDir, "report.json")
	if err := writeJSON(reportPath, report); err != nil {
		return nil, err
	}
	return &Result{
		Report: report, Provenance: prov, Indexes: indexes,
		Resumed: resumed, OutputSHA: sha, InputSHA: inputSHA, ManifestSHA: manifestSHA,
	}, nil
}

// prepare generates fixtures when the manifest is absent or regeneration was
// requested, and returns the manifest digest plus per-camera source digests.
