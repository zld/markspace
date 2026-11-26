package demo

import (
	"audionorm/internal/audio"
	"audionorm/internal/ingest"
	"audionorm/internal/manifest"
	"audionorm/internal/task"
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

// ScenarioResult is the terminal outcome of one demo scenario with the
// exact fields the acceptance criteria check.
type ScenarioResult struct {
	Name             string
	OK               bool
	Details          []string
	Samples          int
	OutputRMSDB      float64
	OutputPeakDB     float64
	Threshold        string
	Duplicates       uint64
	MissingSamples   uint64
	Interval         string
	BudgetDeviation  float64
	Status           task.Status
	HoldOffset       int64
	CheckpointReused bool
	ManifestSHA      string
}

// Runner owns the shared work directory and decoder for all scenarios.
type Runner struct {
	work string
	dec  *audio.Decoder
	doc  *manifest.Document
}

// NewRunner prepares a runner rooted at work.
func NewRunner(work string) (*Runner, error) {
	dec, err := audio.NewDecoder()
	if err != nil {
		return nil, err
	}
	return &Runner{work: work, dec: dec}, nil
}

func spec() task.Spec {
	return task.Spec{SampleRate: 48000, Channels: 2, TargetRMS: audio.TargetRMSDBFS, PeakCeilDB: audio.PeakCeilingDB}
}

// RunAll executes the five required scenarios and returns their results.
// A non-nil error means infrastructure failed; per-scenario assertion
// failures are reported inside ScenarioResult.OK.
func (r *Runner) RunAll(ctx context.Context) ([]ScenarioResult, error) {
	var results []ScenarioResult

	normal, err := r.scenarioNormal(ctx)
	if err != nil {
		return nil, fmt.Errorf("normal scenario: %w", err)
	}
	results = append(results, normal)

	loss, err := r.scenarioLossDup(ctx)
	if err != nil {
		return nil, fmt.Errorf("loss/dup scenario: %w", err)
	}
	results = append(results, loss)

	corrupt, err := r.scenarioCorrupt(ctx)
	if err != nil {
		return nil, fmt.Errorf("corrupt scenario: %w", err)
	}
	results = append(results, corrupt)

	trunc, err := r.scenarioTruncatedIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("truncated-index scenario: %w", err)
	}
	results = append(results, trunc)

	recov, err := r.scenarioRecovery(ctx)
	if err != nil {
		return nil, fmt.Errorf("recovery scenario: %w", err)
	}
	results = append(results, recov)

	// One canonical manifest across every scenario.
	sha, err := r.writeManifest()
	if err != nil {
		return nil, err
	}
	for i := range results {
		results[i].ManifestSHA = sha
	}
	return results, nil
}

// finalize runs layout + loudness on assembled PCM, seals the task and
// merges a manifest entry, returning the measured report and status.
func (r *Runner) finalize(cp *task.Checkpoint, pcm *audio.PCM, res *ingest.Result) (*audio.LoudnessReport, task.Status, error) {
	stereo, err := audio.ToStereo(pcm.Samples, pcm.Channels)
	if err != nil {
		return nil, "", err
	}
	out, rep := audio.NormalizeLoudness(stereo)
	outBytes := f32Bytes(out)

	// Advance the stage machine through normalize then seal, persisting
	// each artifact atomically with its hash. Recovery resumes from the
	// highest stage that still verifies.
	if cp.Stage == task.StageNew {
		if err := cp.RecordStage(r.work, task.StageIngest, "ingest.json",
			[]byte(`{"stage":"ingest"}`)); err != nil {
			return nil, "", err
		}
	}
	if cp.Stage == task.StageIngest {
		if err := cp.RecordStage(r.work, task.StageDecode, "decode.f32le",
			f32Bytes(stereo)); err != nil {
			return nil, "", err
		}
	}
	if cp.Stage == task.StageDecode {
		if err := cp.RecordStage(r.work, task.StageNormalize,
			"normalized.f32le", outBytes); err != nil {
			return nil, "", err
		}
	}

	cp.Loudness = &rep
	cp.Samples = len(out) / 2
	var missing uint64
	for _, g := range res.Gaps {
		missing += g.When.Length()
	}
	cp.MissingSamples = missing
	// Budget deviation compares the target sample count against the
	// *effective* output samples: produced frames minus known gaps.
	target := cp.Range.Length()
	effective := uint64(cp.Samples) - missing
	if target > 0 {
		cp.BudgetDevPct = float64(int64(effective)-int64(target)) / float64(target) * 100
	}
	hasUnknown := false
	for _, e := range res.Errors.All() {
		if !e.Locatable {
			hasUnknown = true
		}
	}
	status := task.StatusPASS
	switch {
	case hasUnknown:
		status = task.StatusHOLD
	case missing > 0:
		status = task.StatusPARTIAL
	}
	cp.Status = status
	cp.Errors = res.Errors.All()
	if err := cp.RecordStage(r.work, task.StageSeal, "output.f32le", outBytes); err != nil {
		return nil, "", err
	}
	if _, err := task.SaveCheckpoint(r.work, cp); err != nil {
		return nil, "", err
	}

	entry := manifest.FromCheckpoint(cp, task.HashBytes(outBytes))
	r.doc, err = manifest.Merge(r.doc, entry)
	if err != nil {
		return nil, "", err
	}
	return &rep, status, nil
}

func (r *Runner) writeManifest() (string, error) {
	if r.doc == nil {
		r.doc = &manifest.Document{SchemaVersion: manifest.SchemaVersion}
	}
	path := filepath.Join(r.work, "manifest.json")
	return manifest.WriteFile(path, r.doc)
}

func f32Bytes(s []float32) []byte {
	out := make([]byte, len(s)*4)
	for i, v := range s {
		bits := math.Float32bits(v)
		out[4*i] = byte(bits)
		out[4*i+1] = byte(bits >> 8)
		out[4*i+2] = byte(bits >> 16)
		out[4*i+3] = byte(bits >> 24)
	}
	return out
}

func gapInterval(res *ingest.Result) string {
	if len(res.Gaps) == 0 {
		return ""
	}
	var b strings.Builder
	for i, g := range res.Gaps {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "[%d,%d)", g.When.Start, g.When.End)
	}
	return b.String()
}
