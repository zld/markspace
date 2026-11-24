// Package pipeline orchestrates parse, jitter, recovery, sync, mux, index and
// manifest stages, recording a checkpoint after each one.
//
// The run is planned before any output bytes are written: frames are scanned
// and verified in metadata-only mode, audio is ordered and gap-sealed, and the
// segment boundaries are fixed. Because the plan depends only on input bytes
// and configuration hashes, a resumed run reproduces the same segment layout.
// Every output file is written to a sibling temporary file and renamed into
// place, so a crash leaves no partial segment behind.
package pipeline

import (
	"os"
	"path/filepath"
	"strings"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/frames"
	"mediarun/internal/provenance"
	"mediarun/internal/recovery"
	rtpparse "mediarun/internal/rtp"
	"mediarun/internal/syncer"
)

// Inputs names the ingest artifacts for one run.
type Inputs struct {
	FramesPath string
	AudioPath  string
}

// hashes caches the ingest digests so a resumed run does not need to re-read
// the whole file before checking checkpoints.
type hashes struct {
	frames  string
	audio   string
	baseRTP uint32
}

// Runner executes one pipeline run.
type Runner struct {
	cfg    Config
	dir    string
	inputs Inputs
	hashes hashes

	cp      *Checkpoint
	resumed bool
	// previous is the manifest from an earlier run in the same directory. It
	// is the baseline for idempotency: a repeated run must reproduce its
	// segment digests exactly.
	previous *provenance.Manifest

	runtime runtimeState

	stages    []provenance.StageSummary
	segments  []provenance.Segment
	faults    fault.List
	timeline  *clock.Timeline
	records   []*frames.Record
	units     []recovery.Unit
	plans     []SegmentPlan
	metrics   provenance.Metrics
	segHashes []string
}

type runtimeState struct {
	audioFaults     fault.List
	placeFaults     fault.List
	audioIncomplete bool
	skippedStages   []string
	resumedAt       string
}

// New validates configuration and prepares a runner over dir.
func New(dir string, inputs Inputs, cfg Config) (*Runner, error) {
	if dir == "" {
		return nil, fault.New(fault.StagePipeline, fault.KindConfig, "output directory is required")
	}
	if inputs.FramesPath == "" || inputs.AudioPath == "" {
		return nil, fault.New(fault.StagePipeline, fault.KindConfig, "frames and audio inputs are required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fault.New(fault.StagePipeline, fault.KindConfig, "%v", err)
	}
	tl, err := clock.NewTimeline(cfg.Timescale)
	if err != nil {
		return nil, fault.New(fault.StagePipeline, fault.KindConfig, "%v", err)
	}
	return &Runner{cfg: cfg, dir: dir, inputs: inputs, timeline: tl}, nil
}

// Resume enables checkpoint resumption. When either the config hash or the
// validated input digests differ from the stored checkpoint, every stage is
// invalidated and recomputed.
func (r *Runner) Resume(on bool) error {
	if !on {
		return nil
	}
	if err := r.hashInputs(); err != nil {
		return err
	}
	cp, ok := LoadCheckpoint(r.dir, r.cfg.Hash(), r.inputHash())
	r.cp = cp
	r.resumed = ok
	if ok {
		r.runtime.skippedStages = append([]string(nil), cp.Done...)
	}
	// Report the resume stage whenever a checkpoint file was present: a
	// complete run reports "complete", a partly finished one reports its next
	// stage, and an invalidated one reports the first stage. A directory with
	// no checkpoint is a fresh run and reports nothing.
	if cp.Exists {
		r.runtime.resumedAt = cp.ResumeStage()
	}
	return nil
}

// PreviousManifest returns the manifest found in the run directory before this
// run started, or nil when there was none. It is the idempotency baseline.
func (r *Runner) PreviousManifest() *provenance.Manifest { return r.previous }

// Checkpoint exposes the checkpoint for reporting.
func (r *Runner) Checkpoint() *Checkpoint { return r.cp }

// ResumedStage returns the stage the run resumed at, if any.
func (r *Runner) ResumedStage() string { return r.runtime.resumedAt }

// Run executes or resumes the pipeline and returns the manifest.
func (r *Runner) Run() (*provenance.Manifest, error) {
	if err := r.hashInputs(); err != nil {
		return nil, err
	}
	if r.cp == nil {
		cp, ok := LoadCheckpoint(r.dir, r.cfg.Hash(), r.inputHash())
		r.cp = cp
		r.resumed = ok
		if ok {
			r.runtime.skippedStages = append([]string(nil), cp.Done...)
			r.runtime.resumedAt = cp.ResumeStage()
		}
	}
	if m, err := provenance.Load(filepath.Join(r.dir, ManifestName)); err == nil {
		r.previous = m
	}
	segmentsDir := filepath.Join(r.dir, SegmentsDir)
	if err := os.MkdirAll(segmentsDir, 0o755); err != nil {
		return nil, fault.Wrap(fault.StagePipeline, fault.KindIO, err, "create segments directory")
	}
	// A previous crash can leave a partially written temporary file behind.
	// Removing them up front keeps the output directory reviewable and stops
	// stale bytes from being mistaken for an output.
	removeStaleTemps(segmentsDir)
	if stats, err := rtpparse.ReadStats(audioStatsPath(r.dir)); err == nil {
		r.hashes.baseRTP = stats.BaseRTP
	}

	// parse: the scan is cheap relative to muxing and is required to rebuild
	// the plan, so it always runs; the checkpoint still records the digest so
	// a changed input invalidates later stages.
	recs, err := r.runParse()
	if err != nil {
		return nil, err
	}
	r.records = recs

	// jitter + recovery are a single pass over the audio sidecar
	units, stats, err := r.runAudio()
	if err != nil {
		return nil, err
	}
	r.units = units
	r.metrics.Frames = len(r.records)
	r.metrics.AudioPackets = stats.Received
	r.metrics.DroppedAudio = stats.Duplicates + stats.Late + stats.Overflow

	placed, placeFaults := syncer.Place(r.units, r.timeline, r.syncConfig())
	r.runtime.placeFaults = placeFaults
	r.plans = selectSegments(r.records, placed, r.cfg.SegmentCount)
	if len(r.plans) == 0 {
		return nil, fault.New(fault.StagePipeline, fault.KindIncomplete, "no frames available to segment")
	}
	if err := r.executeSegments(); err != nil {
		return nil, err
	}
	r.metrics.ParseErrors = countParseFaults(r.faults)
	m, err := r.buildManifest()
	if err != nil {
		return nil, err
	}
	m.Metrics.RecoveredVideo = r.metrics.RecoveredVideo
	if err := r.writeManifest(m); err != nil {
		return nil, err
	}
	return m, nil
}

// inputHash folds both ingest digests into the checkpoint validity key.
func (r *Runner) inputHash() string {
	return stageHash("inputs", "", r.hashes.frames, r.hashes.audio)
}

// hashInputs records the ingest digests and precomputes the RTP base timestamp.
func (r *Runner) hashInputs() error {
	fh, _, err := provenance.HashFile(r.inputs.FramesPath)
	if err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "hash %s", r.inputs.FramesPath)
	}
	ah, _, err := provenance.HashFile(r.inputs.AudioPath)
	if err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "hash %s", r.inputs.AudioPath)
	}
	r.hashes.frames = fh
	r.hashes.audio = ah
	return nil
}

// syncConfig builds the syncer configuration, including the declared RTP base
// timestamp that distinguishes a late audio start from an early one.
func (r *Runner) syncConfig() syncerConfig {
	return syncerConfig{
		ToleranceTicks:   r.cfg.ToleranceTicks,
		AudioClockRate:   r.cfg.AudioClockRate,
		AudioOffsetTicks: r.cfg.SyncOffsetTicks,
		BaseRTP:          r.hashes.baseRTP,
		BaseTick:         0,
		AudioTrack:       2,
	}
}

// recordStage appends a completed stage summary and persists the checkpoint.
func (r *Runner) recordStage(summary provenance.StageSummary) error {
	if r.cp == nil {
		r.cp = newCheckpoint(r.cfg.Hash(), r.inputHash())
	}
	r.stages = append(r.stages, summary)
	for _, f := range summary.Faults {
		r.faults = append(r.faults, &fault.Error{
			Stage:       fault.Stage(f.Stage),
			Kind:        fault.Kind(f.Kind),
			Track:       f.Track,
			Offset:      f.Offset,
			Record:      f.Record,
			Detail:      f.Detail,
			Recoverable: f.Recoverable,
		})
	}
	return r.cp.Mark(r.dir, summary.Name, summary.OutputHash)
}

// removeStaleTemps deletes abandoned temporary files from a directory.
func removeStaleTemps(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) > 0 && name[0] == '.' && strings.HasSuffix(name, ".tmp") {
			os.Remove(filepath.Join(dir, name))
		}
	}
}

func hashIfExists(path string) string {
	if !fileExists(path) {
		return ""
	}
	sum, _, err := provenance.HashFile(path)
	if err != nil {
		return ""
	}
	return sum
}
