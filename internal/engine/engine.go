// Package engine is the reusable local media-processing core. It wires the
// manifest, cache fingerprint, frame index, planner, worker pool, journal and
// independent validator into one resumable run. The command entry point only
// generates fixtures and calls Run; all delivery logic lives here.
package engine

import (
	"errors"
	"os"
	"path/filepath"

	"continuity/internal/cache"
	"continuity/internal/fixture"
	"continuity/internal/index"
	"continuity/internal/journal"
	"continuity/internal/manifest"
	"continuity/internal/model"
	"continuity/internal/planner"
	"continuity/internal/transform"
	"continuity/internal/validate"
)

// ErrInterrupted is returned when a run stops deliberately at InterruptAfter,
// with every frame up to and including that frame committed and validated.
var ErrInterrupted = errors.New("engine: run interrupted after configured frame")

// Config controls one processing run. Generation and mutation are intentionally
// outside the engine: callers prepare root/input before calling Run.
type Config struct {
	Workspace         string
	Threads           int
	MaxOutputBytes    int64
	LoudnessTolerance float64
	// InterruptAfter > 0 stops the run once that frame number is validated.
	InterruptAfter int
	// Resume continues from the last validated frame instead of failing on an
	// existing journal.
	Resume bool
}

// Summary is the independently checkable result of a run.
type Summary struct {
	Discovered           int
	Planned              int
	Processed            int
	Validated            int
	ResumeFrom           model.FrameNumber
	ManifestFrames       int
	ManifestSHA256       string
	OutputSHA256         string
	ContentInvalidations int
	IndexInvalidations   int
	CacheReason          cache.Reason
	Comparison           string
	ChangedHashes        int
}

// Run executes one resumable processing pass over the workspace.
func Run(cfg Config) (*Summary, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	root := cfg.Workspace

	jrnl, err := journal.Open(root)
	if err != nil {
		return nil, err
	}
	if err = jrnl.AssertReusable(); err != nil {
		return nil, err
	}
	// Any frame left processing/processed but never validated by an
	// interrupted run goes back to planned so its temporary output is redone.
	if err = jrnl.ResetInProgress(); err != nil {
		return nil, err
	}
	// Temporary, never-validated outputs are never trusted as committed work.
	_ = os.RemoveAll(mustPath(root, filepath.Join(validate.OutputDir, "tmp")))

	frames, err := manifest.Read(root)
	if err != nil {
		return nil, err
	}
	total := len(frames)
	resumeFrom := jrnl.ResumePoint(total)

	fp, counters, reason, err := evaluateCache(root, frames)
	if err != nil {
		return nil, err
	}

	idx, err := index.Build(root, fixture.ContainerName, fp.MetadataVersion, frames)
	if err != nil {
		return nil, err
	}
	_ = idx
	for i := range frames {
		if err = manifest.VerifyPayload(&frames[i]); err != nil {
			return nil, err
		}
		if jrnl.StateOf(frames[i].Number) == journal.StateDiscovered {
			if err = jrnl.Transition(frames[i].Number, frames[i].Track, journal.StateDiscovered, ""); err != nil {
				return nil, err
			}
		}
	}

	plan, err := planner.Plan(planner.Constraints{
		Threads:           cfg.Threads,
		MaxOutputBytes:    cfg.MaxOutputBytes,
		LoudnessTolerance: cfg.LoudnessTolerance,
		FrameCount:        total,
		FrameDurationNS:   fixture.FrameNanos,
		SourceRate:        frames[0].SampleRate,
	})
	if err != nil {
		return nil, err
	}
	if err = plan.MustHaveSelection(); err != nil {
		// No feasible candidate: the pool is never started.
		return nil, err
	}
	for i := range frames {
		if jrnl.StateOf(frames[i].Number) == journal.StateDiscovered {
			if err = jrnl.Transition(frames[i].Number, frames[i].Track, journal.StatePlanned, ""); err != nil {
				return nil, err
			}
		}
	}

	sel := plan.Selected
	tp := transform.Params{
		SourceRate: frames[0].SampleRate,
		TargetRate: sel.SampleRate,
		Source:     frames[0].Channels,
		Target:     sel.Channels,
		TargetLUFS: sel.TargetLUFS,
	}

	if err = processRemaining(cfg, jrnl, frames, tp); err != nil {
		if errors.Is(err, ErrInterrupted) {
			return summarize(jrnl, counters, reason, resumeFrom, total, err)
		}
		return nil, err
	}

	m, report, err := commitAndVerify(root, frames, sel)
	if err != nil {
		_ = validate.WriteReport(root, report)
		return nil, err
	}
	if err = validate.WriteReport(root, report); err != nil {
		return nil, err
	}

	comparison, changed, err := compareBaseline(root, m)
	if err != nil {
		return nil, err
	}
	return summarizeFull(jrnl, counters, reason, resumeFrom, m, comparison, changed)
}

func validateConfig(cfg Config) error {
	if cfg.Workspace == "" {
		return model.NewError(model.StageWorkspace, 0, 0, "workspace path is required")
	}
	if cfg.Threads < 1 {
		return model.NewError(model.StagePlanner, 0, 0, "threads must be at least 1")
	}
	if cfg.MaxOutputBytes < 0 {
		return model.NewError(model.StagePlanner, 0, 0, "max output bytes cannot be negative")
	}
	if cfg.LoudnessTolerance < 0 {
		return model.NewError(model.StagePlanner, 0, 0, "loudness tolerance cannot be negative")
	}
	return nil
}

func mustPath(root, rel string) string {
	p, err := model.SafeJoin(root, rel)
	if err != nil {
		panic(err)
	}
	return p
}

// SeedBaseline records the current input's fingerprint without running the
// pipeline. The demo adapter calls it before applying a mutation so the
// subsequent run can report the exact invalidation caused by that mutation.
func SeedBaseline(root string) error {
	containerRel := filepath.Join(fixture.InputDir, fixture.ContainerName)
	containerPath, err := model.SafeJoin(root, containerRel)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(containerPath)
	if err != nil {
		return model.WrapError(model.StageCache, 0, 0, "reading container", err)
	}
	containerVersion, _, err := fixture.ContainerHeader(data)
	if err != nil {
		return model.WrapError(model.StageCache, 0, 0, "reading container version", err)
	}
	contentHash, err := fixture.ContentHash(data)
	if err != nil {
		return err
	}
	meta, err := readMetadata(root)
	if err != nil {
		return err
	}
	store, err := cache.LoadStore(root)
	if err != nil {
		return err
	}
	var counters cache.Counters
	store.Evaluate(cache.Fingerprint{
		Path:             containerRel,
		ContentSHA256:    contentHash,
		ContainerVersion: containerVersion,
		MetadataVersion:  meta.MetadataVersion,
	}, &counters)
	return store.Save(root)
}
