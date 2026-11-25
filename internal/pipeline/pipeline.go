// Package pipeline wires the replay phases together. It owns stage ordering,
// the digest trail, cache accounting and failure recovery, so the command layer
// only parses arguments and prints reports.
package pipeline

import (
	"errors"
	"os"
	"path/filepath"

	"replaycheck/internal/fixtures"
	"replaycheck/internal/ingest"
	"replaycheck/internal/job"
	"replaycheck/internal/model"
	"replaycheck/internal/policy"
	"replaycheck/internal/report"
	"replaycheck/internal/store"
)

// Config is the resolved command configuration.
type Config struct {
	FixtureDir string
	SiteDir    string // all persisted state: sources, cache, blobs, state.json
	Kind       fixtures.Kind
	Budget     policy.Budget
	Resume     bool
	Verify     bool
	Limits     ingest.Limits
	// FailAt interrupts the run before this phase, so recovery is exercised
	// by the acceptance command rather than by chance.
	FailAt model.Stage
}

// Result summarizes a completed run.
type Result struct {
	Fixture        string     `json:"fixture"`
	Stages         int        `json:"stages"`
	Segments       int        `json:"segments"`
	ManifestPath   string     `json:"manifestPath"`
	ManifestDigest string     `json:"manifestDigest"`
	TotalBytes     int64      `json:"totalBytes"`
	MinQualityPPM  policy.PPM `json:"minQualityPpm"`
	Valid          bool       `json:"valid"`
	CacheHits      int        `json:"cacheHits"`
	DiffCount      int        `json:"diffCount"`
	SourceDigest   string     `json:"sourceDigest"`
	WrapCount      uint64     `json:"wrapCount"`
	RemainderNanos int64      `json:"remainderNanos"`
	ResumedFrom    string     `json:"resumedFrom,omitempty"`
}

// Run executes the pipeline for one fixture, emitting JSON lines as it goes.
func Run(cfg Config, out *report.Writer) (*Result, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.SiteDir, 0o755); err != nil {
		return nil, model.WrapError(model.StageIngest, "site", model.TrackRef{}, 0, "cannot create site dir", err)
	}
	if cfg.Kind == "" {
		cfg.Kind = fixtures.KindNormal
	}
	fx, err := fixtures.Generate(cfg.FixtureDir, cfg.Kind)
	if err != nil {
		return nil, model.WrapError(model.StageIngest, "fixture", model.TrackRef{}, 0, "cannot generate fixture", err)
	}
	out.Emit(report.KindFixture, map[string]any{
		"kind": string(fx.Kind), "media": fx.MediaPath, "events": fx.EventsPath,
		"clockRate": fx.ClockRate, "expect": fx.Expect,
	})

	sources := store.NewSourceStore(filepath.Join(cfg.SiteDir, "sources"))
	mediaAsset, err := sources.Put("media.mp4", fx.MediaPath)
	if err != nil {
		return nil, err
	}
	eventAsset, err := sources.Put("events.rtp", fx.EventsPath)
	if err != nil {
		return nil, err
	}

	stateStore := job.NewStore(cfg.SiteDir)
	state, lerr := stateStore.Load()
	if lerr != nil {
		if !errors.Is(lerr, job.ErrNoState) {
			return nil, lerr
		}
		state = nil
	}
	if state == nil || state.Fixture != string(fx.Kind) || state.SourceSum != mediaAsset.Digest || state.EventSum != eventAsset.Digest {
		state = job.NewState(string(fx.Kind), mediaAsset.Digest, eventAsset.Digest)
	}
	runner := &job.Runner{State: state, Resume: cfg.Resume}
	resumeAt := runner.ResumeAt(mediaAsset.Digest, eventAsset.Digest)
	if cfg.Resume && (state.LastOK != "" || state.FailedAt != "") {
		out.Emit(report.KindResume, map[string]any{
			"resumedFrom": resumeAt, "lastOk": state.LastOK, "failedAt": state.FailedAt,
			"sourceDigest": mediaAsset.Digest,
		})
	}

	r := &run{
		cfg: cfg, out: out, runner: runner, resumeAt: resumeAt,
		clockRate: fx.ClockRate,
		mediaPath: mediaAsset.Path, eventPath: eventAsset.Path,
		mediaAsset: mediaAsset, eventAsset: eventAsset,
		siteDir: cfg.SiteDir,
	}
	err = r.execute()
	if err != nil {
		var me *model.Error
		if errors.As(err, &me) {
			runner.Fail(me.Stage, me.Offset, err)
			out.Emit(report.KindError, map[string]any{
				"stage": me.Stage, "track": me.Track.String(), "trackId": me.Track.ID,
				"offset": me.Offset, "message": me.Error(),
			})
		} else {
			out.Emit(report.KindError, map[string]any{"stage": "", "offset": 0, "message": err.Error()})
		}
		stateStore.Save(runner.State)
		return nil, err
	}
	stateStore.Save(runner.State)
	return r.result, nil
}

func validateConfig(cfg Config) error {
	if cfg.FixtureDir == "" {
		return model.NewError(model.StageIngest, "config", model.TrackRef{}, 0, "fixture directory is required")
	}
	if cfg.SiteDir == "" {
		return model.NewError(model.StageIngest, "config", model.TrackRef{}, 0, "site directory is required")
	}
	if err := cfg.Budget.Validate(); err != nil {
		return err
	}
	if cfg.Limits.MaxBytes <= 0 {
		return model.NewError(model.StageIngest, "config", model.TrackRef{}, 0, "byte limit must be positive")
	}
	return nil
}
