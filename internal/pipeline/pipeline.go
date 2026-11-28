// Package pipeline orchestrates the parse -> compose -> export -> verify
// stages into one resumable run. It owns stage transitions, per-layer
// checkpoints, on-disk frame records and the sealed manifest; the subsystem
// packages stay independently testable and free of file orchestration.
package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"mediapack/internal/audio"
	"mediapack/internal/fanout"
	"mediapack/internal/fixture"
	"mediapack/internal/ingest"
	"mediapack/internal/manifest"
	"mediapack/internal/model"
	"mediapack/internal/recovery"
	"mediapack/internal/render"
	"mediapack/internal/timeline"
)

// Event is one user-visible pipeline step, printed by the CLI.
type Event struct {
	Stage   model.Stage
	Message string
}

// Result summarizes a run for the terminal report.
type Result struct {
	Stage         model.Stage
	Interrupted   bool
	InputDigest   string
	ManifestHash  string
	DurationTicks int64
	MaxAlignTicks int64
	FrameCount    int
	Layers        []LayerSummary
	Audio         []audio.TrackStats
}

// LayerSummary is the per-layer part of the result report.
type LayerSummary struct {
	Name          string
	Frames        int
	LastSegment   int
	LastFrame     int
	RecoveredGaps int
	OpenGaps      int
	Complete      bool
}

// Options configures a run.
type Options struct {
	OutDir         string
	Seed           int64
	Corrupt        string // "" or "mxf-klv-length"
	InterruptAfter int    // segments per layer to anchor before interrupting; 0 = none
	Resume         bool
}

// targetSampleRate is the single normalized audio clock target.
const targetSampleRate = 48000

// run holds the mutable state shared across one execution.
type run struct {
	opts   Options
	emit   func(Event)
	cp     *recovery.Checkpoint
	layers []fanout.LayerSpec
}

// Run executes the pipeline. The emitter may be nil. A returned error is always
// either a *model.FieldError or wraps one.
func Run(opts Options, emit func(Event)) (*Result, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	r := &run{opts: opts, emit: emit}
	return r.execute()
}

// execute implements the full state machine, branching only for resume.
func (r *run) execute() (*Result, error) {
	// 1. Build in-memory samples and reject corruption before any disk write.
	spec := fixture.DemoSpec(r.opts.Seed)
	mxfData := fixture.BuildMXF(spec)
	if r.opts.Corrupt == "mxf-klv-length" {
		mxfData = fixture.CorruptMXFKLVLength(mxfData)
	}
	mp4Data := fixture.BuildMP4(spec)

	mp4Name := "sample.mp4"
	mxfName := "sample.mxf"
	if _, err := ingest.ParseMP4Reader(bytes.NewReader(mp4Data), mp4Name); err != nil {
		return nil, err
	}
	if _, err := ingest.ParseMXFReader(bytes.NewReader(mxfData), mxfName); err != nil {
		// Corrupt input: never touch the output directory.
		return nil, err
	}

	// 2. Guard previously verified artifacts.
	if err := manifest.GuardVerifiedArtifact(r.opts.OutDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(r.opts.OutDir, 0o755); err != nil {
		return nil, err
	}
	// Persist the source samples for traceability (they parsed successfully).
	if err := r.writeSource(mp4Name, mp4Data); err != nil {
		return nil, err
	}
	if err := r.writeSource(mxfName, mxfData); err != nil {
		return nil, err
	}

	// 3. Parse both containers for real, then compose on the shared clock.
	mp4C, err := ingest.ParseMP4Reader(bytes.NewReader(mp4Data), mp4Name)
	if err != nil {
		return nil, err
	}
	mxfC, err := ingest.ParseMXFReader(bytes.NewReader(mxfData), mxfName)
	if err != nil {
		return nil, err
	}
	r.emit(Event{Stage: model.StageCreated, Message: "samples generated and parsed in memory"})
	// MP4 anchors the timeline; MXF descriptions of the same logical media are
	// parsed for cross-format validation but not duplicated on the clock.
	tl, err := timeline.ComposeUnique(mp4C, mxfC)
	if err != nil {
		return nil, err
	}

	logicalTracks := ingest.DeduplicateTracks(mp4C, mxfC)
	inputDigest, err := render.SummaryDigest(logicalTracks)
	if err != nil {
		return nil, err
	}

	// 4. Checkpoint: load on resume, otherwise create at created stage.
	if r.opts.Resume {
		cp, err := recovery.Load(r.opts.OutDir)
		if err != nil {
			return nil, model.WrapField(model.KindInvalidState, r.opts.OutDir, -1,
				"checkpoint", "cannot resume without a checkpoint", err)
		}
		r.opts.Seed = cp.Seed
		if !cp.MatchesInput(r.opts.Seed, inputDigest) {
			return nil, model.NewFieldError(model.KindChecksum, r.opts.OutDir, -1,
				"checkpoint/input", "resume input does not match checkpoint")
		}
		r.cp = cp
		r.emit(Event{Stage: r.cp.Stage, Message: "resumed from checkpoint"})
	} else {
		r.cp = recovery.New(r.opts.Seed, inputDigest)
		if err := r.cp.Transition(model.StageParsed); err != nil {
			return nil, err
		}
		r.cp.Transition(model.StageComposed)
	}

	// 5. Audio normalization: an unsupported layout fails one track only.
	norm := audio.NewNormalizer(targetSampleRate)
	for _, seg := range tl.AudioSegments() {
		if _, err := norm.Normalize(seg.GlobalIndex, seg.Track); err != nil {
			r.emit(Event{Stage: model.StageComposed,
				Message: "audio track " + fmt.Sprint(seg.GlobalIndex) + " failed normalization: " + err.Error()})
		}
	}

	r.layers = buildLayers()
	for _, l := range r.layers {
		r.cp.EnsureLayer(l.Name)
	}

	renderer := render.NewRenderer(inputDigest)
	res, err := r.exportLayers(renderer, tl)
	if err != nil {
		return nil, err
	}
	if r.opts.InterruptAfter > 0 && !r.opts.Resume {
		if err := r.cp.Save(r.opts.OutDir); err != nil {
			return nil, err
		}
		return r.interruptedResult(tl, res, norm.Stats()), nil
	}
	// 6. Seal the manifest and verify.
	return r.finalize(tl, res, norm.Stats())
}

func buildLayers() []fanout.LayerSpec {
	shapes := fixture.DemoLayers()
	out := make([]fanout.LayerSpec, len(shapes))
	for i, s := range shapes {
		out[i] = fanout.LayerSpec{
			Name: s.Name, BitrateKbps: s.BitrateKbps, Width: s.Width,
			Height: s.Height, SSRC: s.SSRC, FramesPerSeg: s.FramesPerSeg,
		}
	}
	return out
}

func (r *run) writeSource(name string, data []byte) error {
	p, err := manifest.SafePath(r.opts.OutDir, filepath.Join("inputs", name))
	if err != nil {
		return err
	}
	return writeAtomicFile(p, data)
}

func writeAtomicFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
