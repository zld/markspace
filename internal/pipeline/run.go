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
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	if !ok {
		return nil
	}
	r.runtime.skippedStages = append([]string(nil), cp.Done...)
	// A complete checkpoint resumes at "complete"; otherwise report the first
	// stage that still needs work.
	r.runtime.resumedAt = cp.ResumeStage()
	return nil
}

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

	// parse
	if r.cp.IsDone("parse") {
		recs, err := r.runParse()
		if err != nil {
			return nil, err
		}
		r.records = recs
	} else {
		recs, err := r.runParse()
		if err != nil {
			return nil, err
		}
		r.records = recs
	}

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

// buildManifest assembles the final document with stable ordering.
func (r *Runner) buildManifest() (*provenance.Manifest, error) {
	inputs := []provenance.Input{}
	for _, pair := range []struct{ name, path, hash string }{
		{"frames", r.inputs.FramesPath, r.hashes.frames},
		{"audio", r.inputs.AudioPath, r.hashes.audio},
	} {
		if pair.hash == "" {
			continue
		}
		info, err := os.Stat(pair.path)
		if err != nil {
			return nil, fault.Wrap(fault.StagePipeline, fault.KindIO, err, "stat %s", pair.path)
		}
		inputs = append(inputs, provenance.Input{
			Name: pair.name, Path: filepath.Base(pair.path), Bytes: info.Size(), SHA256: pair.hash,
		})
	}
	m := &provenance.Manifest{
		Version:    provenance.Version,
		Tool:       "mediarun",
		ConfigHash: r.cfg.Hash(),
		Timescale:  r.cfg.Timescale,
		Inputs:     inputs,
		Stages:     r.stages,
		Segments:   r.segments,
		Faults:     r.faultRecords(),
		Metrics:    r.metrics,
	}
	if n := len(r.records); n > 0 {
		m.TimelineEnd = int64(r.records[n-1].Meta.PTS)
	}
	sort.SliceStable(m.Stages, func(i, j int) bool {
		return stageIndex(m.Stages[i].Name) < stageIndex(m.Stages[j].Name)
	})
	return m, nil
}

func (r *Runner) faultRecords() []provenance.FaultRecord {
	out := make([]provenance.FaultRecord, 0, len(r.faults))
	for _, e := range r.faults {
		out = append(out, provenance.FromFault(e))
	}
	provenance.SortFaults(out)
	return out
}

// writeManifest serializes and atomically replaces manifest.json.
func (r *Runner) writeManifest(m *provenance.Manifest) error {
	path := filepath.Join(r.dir, ManifestName)
	if err := provenance.WriteAtomic(path, m); err != nil {
		return err
	}
	return r.cp.Mark(r.dir, "manifest", provenance.HashBytesHex(mustJSON(m)))
}

func mustJSON(v any) []byte {
	b, err := provenance.CanonicalJSON(v)
	if err != nil {
		return nil
	}
	return b
}

// recordsDigest hashes only metadata so metadata-only records hash identically
// to full ones.
func recordsDigest(recs []*frames.Record) string {
	h := newDigest()
	for _, rec := range recs {
		fmt.Fprintf(h, "%d|%d|%d|%d|%x|%d\n", rec.Offset, rec.Meta.PTS, rec.Meta.DTS, rec.Meta.Flags, rec.Meta.Checksum[:4], rec.Meta.Size)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// audioDigest hashes the sealed unit sequence.
func audioDigest(units []recovery.Unit) string {
	h := newDigest()
	for _, u := range units {
		fmt.Fprintf(h, "%d|%d|%d|%t\n", u.Seq, u.RTPTimestamp, u.TickSpan, u.Silence)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isEOF(err error) bool { return errors.Is(err, io.EOF) }

// countParseFaults counts faults raised by the parse and checksum stages,
// which is what the acceptance line reports as parse_errors.
func countParseFaults(fs fault.List) int {
	n := 0
	for _, e := range fs {
		if e.Stage == fault.StageParse || e.Stage == fault.StageChecksum {
			n++
		}
	}
	return n
}

// audioStatsPath is the sidecar summary the generator writes next to the
// audio fixture. The pipeline records its digest so the manifest describes
// every input, including the declared RTP base timestamp.
func audioStatsPath(dir string) string { return filepath.Join(dir, "audio.stats") }

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
