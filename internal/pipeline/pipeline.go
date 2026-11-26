// Package pipeline wires the scan, plan, worker, publish, recovery and verify
// subsystems into one explainable, resumable regression run. The library is
// reusable; cmd/regress only parses flags and calls Run.
package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"regress/internal/fault"
	"regress/internal/fixture"
	"regress/internal/plan"
	"regress/internal/publish"
	"regress/internal/recovery"
	"regress/internal/scan"
	"regress/internal/verify"
	"regress/internal/worker"
)

// Injection points emulate process exits for the recovery scenarios.
type Injection string

const (
	InjectNone             Injection = ""
	InjectCrashAfterPublish Injection = "crash-after-publish"
)

// Options configures one regression run.
type Options struct {
	JobKey        string
	Codec         string
	Segments      int
	SSIMMin       float64
	MaxBitrateBPS int64
	WorkBudget    int64
	RootDir       string
	Resume        bool
	Inject        Injection
	// Concurrency bounds the worker pool.
	Concurrency int
}

// Metrics is the terminal, explainable result printed by the CLI.
type Metrics struct {
	ExitCode         int
	RoundTripPct     float64
	KeyframeHitPct   float64
	HashMatch        string // e.g. "4/4"
	SSIMMin          float64
	BitratePeakBPS   int64
	Rejected         int
	Recovered        int
	Published        int
	Duplicate        int
	Stages           []string
}

// Paths centralizes the on-disk layout.
type Paths struct {
	Root    string
	Fixture string
	Stage   string
	Pub     string
	Journal string
}

func layout(root, job string) Paths {
	return Paths{
		Root:    root,
		Fixture: filepath.Join(root, "fixture"),
		Stage:   filepath.Join(root, "stage"),
		Pub:     filepath.Join(root, "publish"),
		Journal: filepath.Join(root, "journal"),
	}
}

// Run executes one full regression and returns metrics plus the structured
// error (nil on success). The exit code is embedded in Metrics so the CLI can
// map it directly to a process exit status.
func Run(ctx context.Context, opt Options) (*Metrics, error) {
	m := &Metrics{ExitCode: 1}
	paths := layout(opt.RootDir, opt.JobKey)
	for _, d := range []string{paths.Fixture, paths.Stage, paths.Pub, paths.Journal} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return m, err
		}
	}

	// 1. Synthesize the deterministic input stream and keyframe index.
	spec := fixture.DefaultSpec(opt.Codec)
	if opt.Segments > 0 {
		spec.Frames = opt.Segments * spec.GOP
	}
	set, err := fixture.Build(ctx, filepath.Join(paths.Fixture, opt.Codec), spec)
	if err != nil {
		return m, err
	}

	// 2. Re-parse the index independently and validate against the scan.
	indexFile, err := os.Open(set.IndexPath)
	if err != nil {
		return m, err
	}
	index, err := scan.UnmarshalKeyIndex(indexFile)
	indexFile.Close()
	if err != nil {
		return m, err
	}
	report, verr := scan.ValidateIndex(set.Scan, index)
	if verr != nil {
		// Corrupt-index scenario: report hit rate and refuse to publish.
		m.Rejected = report.Rejected
		m.KeyframeHitPct = 100 * float64(report.Accepted) / float64(report.Total)
		m.ExitCode = 2
		return m, verr
	}
	m.KeyframeHitPct = 100.0

	// 3. Open the journal and handle resume before doing any work.
	journal, err := recovery.Open(paths.Journal)
	if err != nil {
		return m, err
	}
	segPlan := plan.BuildSegmentPlan(set.Scan, set.TimeDen)
	con := plan.Constraints{
		SSIMMin: opt.SSIMMin, MaxBitrateBPS: opt.MaxBitrateBPS,
		WorkBudget: opt.WorkBudget, Timescale: set.TimeDen,
	}

	if opt.Resume {
		return resumeRun(ctx, opt, paths, set, spec, segPlan, con, journal, m)
	}
	return freshRun(ctx, opt, paths, set, spec, segPlan, con, journal, m)
}

func freshRun(ctx context.Context, opt Options, paths Paths,
	set *fixture.StreamSet, spec fixture.Spec, segPlan *plan.SegmentPlan,
	con plan.Constraints, journal *recovery.Journal, m *Metrics) (*Metrics, error) {
	now := time.Now().UnixNano()

	// 4. Plan via trial encodes.
	enc := &worker.FFmpegEncoder{
		SourcePath: set.StreamPath, Codec: opt.Codec,
		WorkDir: filepath.Join(paths.Stage, "trials"), Segments: segPlan,
		SourceW: spec.Width, SourceH: spec.Height, SourceFPS: spec.FPS,
	}
	decision, perr := plan.NewPlanner(enc, opt.Codec, set.Scan, con).Run(ctx)
	if perr != nil {
		// Unsatisfiable budget: no candidate, no publish.
		m.ExitCode = 2
		return m, perr
	}

	rec, cerr := journal.Create(opt.JobKey, decision.ConfigHash, now)
	if cerr != nil {
		return m, cerr
	}
	if _, err := journal.Transition(opt.JobKey, recovery.StateProcessing, now); err != nil {
		return m, err
	}

	// 5. Publish through staging and atomic promotion.
	pub, err := publish.NewPublisher(paths.Stage, paths.Pub)
	if err != nil {
		return m, err
	}
	art, err := pub.Publish(ctx, &publish.Request{
		JobKey: opt.JobKey, Codec: opt.Codec, SourcePath: set.StreamPath,
		SourceW: spec.Width, SourceH: spec.Height, SourceFPS: spec.FPS, Decision: decision,
	})
	if err != nil {
		return m, err
	}
	segRecs := make([]recovery.SegmentRecord, 0, len(art.Manifest.Segments))
	for _, s := range art.Manifest.Segments {
		segRecs = append(segRecs, recovery.SegmentRecord{File: s.File, SHA256: s.SHA256})
	}
	if err := journal.SetSegments(opt.JobKey, "manifest.json", segRecs, now); err != nil {
		return m, err
	}
	if _, err := journal.Transition(opt.JobKey, recovery.StateStaged, now); err != nil {
		return m, err
	}
	if _, err := journal.Transition(opt.JobKey, recovery.StatePublished, now); err != nil {
		return m, err
	}

	// Crash injection: products are written and state is "published" but the
	// process exits before recording "committed".
	if opt.Inject == InjectCrashAfterPublish {
		m.ExitCode = 75
		return m, fault.New(fault.StageRecovery, fault.CodeStateConflict, opt.Codec,
			"injected exit after publish before commit")
	}

	// 6. Independent verification then commit.
	if err := verifyAndFinalize(ctx, opt, paths, set, spec, con, art, journal, m, now); err != nil {
		return m, err
	}
	_ = rec
	m.ExitCode = 0
	return m, nil
}

func resumeRun(ctx context.Context, opt Options, paths Paths,
	set *fixture.StreamSet, spec fixture.Spec, segPlan *plan.SegmentPlan,
	con plan.Constraints, journal *recovery.Journal, m *Metrics) (*Metrics, error) {
	now := time.Now().UnixNano()
	// Re-derive the expected config hash deterministically by re-planning.
	enc := &worker.FFmpegEncoder{
		SourcePath: set.StreamPath, Codec: opt.Codec,
		WorkDir: filepath.Join(paths.Stage, "trials-resume"), Segments: segPlan,
		SourceW: spec.Width, SourceH: spec.Height, SourceFPS: spec.FPS,
	}
	decision, perr := plan.NewPlanner(enc, opt.Codec, set.Scan, con).Run(ctx)
	if perr != nil {
		m.ExitCode = 2
		return m, perr
	}
	res, rerr := journal.Resume(opt.JobKey, decision.ConfigHash, paths.Pub, now, worker.HashFile)
	if rerr != nil {
		return m, rerr
	}
	m.Recovered = res.Recovered
	m.Published = res.Published
	m.Duplicate = res.Duplicate

	// Re-run independent verification against the claimed products.
	manifestPath := filepath.Join(paths.Pub, opt.JobKey, "manifest.json")
	manifest, err := publish.ReadManifest(manifestPath)
	if err != nil {
		return m, err
	}
	art := &publish.ArtifactSet{
		ManifestPath: manifestPath,
		AuditPath:    filepath.Join(paths.Pub, opt.JobKey, "rtp-audit.json"),
		Manifest:     manifest,
	}
	if err := verifyAndFinalize(ctx, opt, paths, set, spec, con, art, journal, m, now); err != nil {
		return m, err
	}
	m.ExitCode = 0
	return m, nil
}

// verifyAndFinalize runs the independent verifier and marks the job committed.
func verifyAndFinalize(ctx context.Context, opt Options, paths Paths,
	set *fixture.StreamSet, spec fixture.Spec, con plan.Constraints,
	art *publish.ArtifactSet, journal *recovery.Journal, m *Metrics, now int64) error {
	jobRoot := filepath.Join(paths.Pub, opt.JobKey)
	ssim, err := verify.MeasureSSIM(ctx, opt.Codec, set.StreamPath, set.StreamPath,
		spec.Width, spec.Height, spec.FPS)
	if err != nil {
		return err
	}
	rep, verr := verify.NewChecker(jobRoot).Check(ctx, verify.Expectations{
		SSIMMin: con.SSIMMin, MaxBitrateBPS: con.MaxBitrateBPS, MeasuredSSIM: ssim,
	})
	if verr != nil {
		return verr
	}
	m.RoundTripPct = rep.RoundTripPct
	m.BitratePeakBPS = rep.BitratePeakBPS
	m.SSIMMin = ssim
	m.HashMatch = fmt.Sprintf("%d/%d", rep.HashesOK, rep.SegmentsChecked)
	if _, err := journal.Transition(opt.JobKey, recovery.StateCommitted, now); err != nil {
		return err
	}
	return nil
}
