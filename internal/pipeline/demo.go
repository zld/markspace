package pipeline

import (
	"fmt"

	"mediad/internal/cache"
	"mediad/internal/fixture"
	"mediad/internal/verify"
)

// DemoConfig controls a full demo invocation.
type DemoConfig struct {
	WorkDir     string
	Scenarios   []string
	FromNanos   int64
	ToNanos     int64
	TargetKbps  int
	DurationSec float64
	Runner      verify.Runner // injectable ffmpeg PSNR runner
}

// Demo generates the material once, materializes every requested scenario and
// runs the pipeline against each. The normal scenario runs twice (cold then
// warm) to demonstrate deterministic cache hits. Rows are returned in a stable
// order for deterministic terminal output and hashing.
func Demo(cfg DemoConfig) ([]*Row, error) {
	if len(cfg.Scenarios) == 0 {
		cfg.Scenarios = fixture.AllScenarios
	}
	ff := fixture.LocalFFmpeg{}
	genDir := cfg.WorkDir + "/generated"
	bundle, err := fixture.GenerateBase(ff, genDir, fixture.Options{
		DurationSec: cfg.DurationSec, TargetKbps: cfg.TargetKbps,
		Width: 320, Height: 240,
	})
	if err != nil {
		return nil, err
	}
	scenarioDir := cfg.WorkDir + "/scenarios"
	paths, err := fixture.Materialize(bundle, scenarioDir, cfg.Scenarios)
	if err != nil {
		return nil, err
	}

	dirs := Dirs{
		Work:     scenarioDir,
		Store:    cfg.WorkDir + "/store",
		Cache:    cfg.WorkDir + "/cache",
		Manifest: cfg.WorkDir, // manifest.Write appends its own manifests/ dir
	}
	params := cache.Params{TargetKbps: cfg.TargetKbps, Pipeline: "mediad-1"}
	rows := make([]*Row, 0, len(cfg.Scenarios)+1)

	ordered := orderedScenarios(cfg.Scenarios)
	for _, sc := range ordered {
		mp4Path := paths[sc]
		switch sc {
		case fixture.ScenarioNormal:
			// Cold pass: derive every segment and build the index.
			cold, err := Run(dirs, bundle, mp4Path, sc+"-cold",
				cfg.FromNanos, cfg.ToNanos, params, cfg.Runner, false)
			if err != nil {
				return nil, err
			}
			// Warm pass: identical input must hit cache, no index rebuild.
			warm, err := Run(dirs, bundle, mp4Path, sc,
				cfg.FromNanos, cfg.ToNanos, params, cfg.Runner, false)
			if err != nil {
				return nil, err
			}
			cold.Scenario = sc + "-cold"
			warm.CacheHit = true
			rows = append(rows, cold, warm)
		case fixture.ScenarioDamage:
			// First derive healthy segments, then corrupt one derived cache
			// entry so only that segment recomputes from its sync boundary.
			if _, err := Run(dirs, bundle, mp4Path, sc+"-prime",
				cfg.FromNanos, cfg.ToNanos, params, cfg.Runner, false); err != nil {
				return nil, err
			}
			row, err := Run(dirs, bundle, mp4Path, sc,
				cfg.FromNanos, cfg.ToNanos, params, cfg.Runner, true)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		case fixture.ScenarioReplace:
			row, err := Run(dirs, bundle, mp4Path, sc,
				cfg.FromNanos, cfg.ToNanos, params, cfg.Runner, false)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		case fixture.ScenarioMetadata:
			// Prime the cache from the base content first, then process the
			// metadata-only variant which must rebuild the index but reuse the
			// unchanged derived segments.
			basePath := paths[fixture.ScenarioNormal]
			if _, err := Run(dirs, bundle, basePath, sc+"-prime",
				cfg.FromNanos, cfg.ToNanos, params, cfg.Runner, false); err != nil {
				return nil, err
			}
			row, err := Run(dirs, bundle, mp4Path, sc,
				cfg.FromNanos, cfg.ToNanos, params, cfg.Runner, false)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		default:
			return nil, fmt.Errorf("pipeline: unknown scenario %q", sc)
		}
	}
	return rows, nil
}

// orderedScenarios returns requested scenarios in canonical order.
func orderedScenarios(want []string) []string {
	wantSet := map[string]bool{}
	for _, s := range want {
		wantSet[s] = true
	}
	var out []string
	for _, s := range fixture.AllScenarios {
		if wantSet[s] {
			out = append(out, s)
		}
	}
	return out
}
