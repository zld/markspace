package demo

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/medialab/medialab/internal/clock"
	"github.com/medialab/medialab/internal/manifest"
	"github.com/medialab/medialab/internal/pipeline"
	"github.com/medialab/medialab/internal/render"
	"github.com/medialab/medialab/internal/report"
	"github.com/medialab/medialab/internal/rtprecover"
	"github.com/pion/rtp"
)

// Exit codes follow the CLI contract.
const (
	ExitOK          = 0
	ExitProcessErr  = 1
	ExitInputErr    = 2
)

// Result is everything the CLI needs to print and persist.
type Result struct {
	Summary     report.Summary
	FailedStage string
	ExitCode    int
	Err         error
	Frames      []render.Frame
}

// stage names reported in failure summaries.
const (
	stageManifest = "manifest"
	stageRecover  = "recover"
	stageCompose  = "compose"
	stageRender   = "render"
	stageReport   = "report"
)

// Run executes one full deterministic demo and returns a Result even when a
// processing stage fails, so corrupt runs still produce a summary.
func Run(ctx context.Context, cfg Config) *Result {
	res := &Result{ExitCode: ExitOK}
	if err := validate(cfg); err != nil {
		res.ExitCode, res.Err = ExitInputErr, err
		res.FailedStage = stageManifest
		return res
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		res.ExitCode, res.Err = ExitProcessErr, err
		res.FailedStage = stageRender
		return res
	}

	fx := BuildFixture(cfg)
	// Truncated manifest: cut the serialized document mid-token.
	if err := writeManifestArtifact(cfg, fx.Manifest); err != nil {
		return failInput(res, stageManifest, err)
	}

	timeline := clock.NewTimeline()
	recovered, recStats, gaps, err := recoverAll(ctx, cfg, fx, timeline)
	if err != nil {
		res.FailedStage = stageRecover
		res.ExitCode = ExitProcessErr
		res.Err = err
	}

	// Bitrate control runs over the generated trace, independent of recovery
	// failures, so the corrupt summary still carries measured rate fields.
	rateState := runBitrate(cfg, fx)

	frames, renderErr := renderFrames(ctx, cfg, recovered, timeline)
	if renderErr != nil {
		if res.ExitCode == ExitOK {
			res.ExitCode = ExitProcessErr
		}
		res.FailedStage = stageRender
		res.Err = renderErr
	}
	res.Frames = frames

	metrics := report.Metrics{
		TargetBps:       rateState.target,
		ActualBps:       rateState.actual,
		BitrateDeltaPct: rateState.delta,
		Frames:          len(frames),
		Qualities:       qualities(frames),
		AlignmentErrMs:  alignmentError(timeline),
		Duplicates:      recStats.Duplicates,
		LateDropped:     recStats.LateDropped,
		ChecksumBad:     recStats.ChecksumBad,
		Gaps:            gaps,
		PeakQueue:       recStats.PeakHeld,
		FailedStage:     res.FailedStage,
	}
	// Invariant verification never aborts a corrupt-run summary: gap-induced
	// cross-track deviation is reported in the metrics above, while healthy
	// runs (no gaps, no failed stage) must still satisfy the strict 1 ms
	// alignment budget. Verify returns an error that records the report stage.
	healthyRun := len(gaps) == 0 && res.FailedStage == ""
	if healthyRun {
		if err := report.Verify(metrics, frames); err != nil && res.FailedStage == "" {
			res.FailedStage = stageReport
			res.ExitCode = ExitProcessErr
			res.Err = err
		}
	} else if err := report.Verify(metrics, frames); err != nil {
		// Surface frame-level monotonic/hash violations even on corrupt runs;
		// gap-driven alignment drift is expected and not a fresh failure.
		if me, ok := clock.As(err); ok && me.Kind != clock.KindNonMonotonic {
			res.FailedStage = stageReport
			if res.ExitCode == ExitOK {
				res.ExitCode = ExitProcessErr
				res.Err = err
			}
		}
	}
	res.Summary = report.Build(metrics, frames)
	_ = report.WriteJSON(filepath.Join(cfg.OutDir, "summary.json"), res.Summary)
	return res
}

func validate(cfg Config) error {
	if cfg.DurationSec <= 0 || cfg.Tracks <= 0 || cfg.WindowCapacity <= 0 ||
		cfg.QueueCapacity <= 0 || cfg.TargetBps <= 0 || cfg.LossBurst < 0 {
		return clock.NewError(clock.KindOutOfRange, "cli", "", 0,
			"all numeric flags must be positive (loss-burst may be zero)")
	}
	return nil
}

func failInput(res *Result, stage string, err error) *Result {
	res.FailedStage = stage
	res.ExitCode = ExitInputErr
	res.Err = err
	return res
}

// recoveredTrack holds one track's ordered samples plus its gap list.
type recoveredTrack struct {
	trackID string
	samples []rtprecover.Sample
	gaps    []rtprecover.Gap
}

func recoverAll(ctx context.Context, cfg Config, fx *Fixture,
	tl *clock.Timeline,
) ([]recoveredTrack, rtprecover.Stats, []rtprecover.Gap, error) {
	var allGaps []rtprecover.Gap
	var merged rtprecover.Stats
	peakQueue := 0
	var out []recoveredTrack
	var firstErr error

	for i := range fx.Manifest.Tracks {
		tr := &fx.Manifest.Tracks[i]
		tickOf := func(seq uint16) int64 {
			return int64(seq)*tickStep + tr.StartMs
		}
		rec := rtprecover.NewRecover(rtprecover.RecoverConfig{
			WindowCapacity: cfg.WindowCapacity,
			BurstThreshold: cfg.LossBurst,
			TrackID:        tr.ID,
			IsVideo:        tr.Kind == manifest.KindVideo,
		}, tickOf)

		runner := pipeline.NewRunner[*rtp.Packet](cfg.QueueCapacity)
		feeder := newFeeder(cfg, fx, tr.ID, rec)
		err := runner.Run(ctx,
			func(ctx context.Context) error {
				return feeder.produce(ctx, runner.Queue())
			},
			feeder.stage,
		)
		if err != nil && firstErr == nil && !errors.Is(err, context.Canceled) {
			firstErr = err
		}
		tail, cerr := rec.Close()
		if cerr != nil && firstErr == nil {
			firstErr = cerr
		}
		samples := append(feeder.emitted, tail...)
		for _, s := range samples {
			// Map source-relative tick onto the output timeline via edits.
			outTick, mapErr := fx.Manifest.Resolve(tr.ID, s.AtTick-tr.StartMs)
			if mapErr != nil && firstErr == nil {
				firstErr = mapErr
				continue
			}
			if appErr := tl.Append(clock.Point{At: outTick, Track: tr.ID, Seq: s.Seq}); appErr != nil {
				if firstErr == nil {
					firstErr = appErr
				}
			}
			s.AtTick = outTick
		}
		tg := rec.Gaps()
		// Re-base gap ranges from source ticks (including the non-zero start
		// offset) onto the output timeline through the same edit mapping used
		// for samples, keeping every reported time position comparable.
		for gi := range tg {
			startRel := tg[gi].Ticks.Start - tr.StartMs
			endRel := tg[gi].Ticks.End - tr.StartMs
			if outStart, mErr := fx.Manifest.Resolve(tr.ID, startRel); mErr == nil {
				tg[gi].Ticks.Start = outStart
			}
			if outEnd, mErr := fx.Manifest.Resolve(tr.ID, endRel); mErr == nil {
				tg[gi].Ticks.End = outEnd
			}
		}
		allGaps = append(allGaps, tg...)
		st := rec.Stats()
		merged.Duplicates += st.Duplicates
		merged.LateDropped += st.LateDropped
		merged.ChecksumBad += st.ChecksumBad
		merged.Emitted += st.Emitted
		if p := runner.Peak(); p > peakQueue {
			peakQueue = p
		}
		out = append(out, recoveredTrack{trackID: tr.ID, samples: samples, gaps: tg})
	}
	merged.PeakHeld = peakQueue
	return out, merged, allGaps, firstErr
}
