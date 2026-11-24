// Package app wires the TS, analysis, fingerprint and manifest subsystems
// into the resumable pipeline DAG. Each node persists an artifact file in the
// run directory in addition to its atomic checkpoint, so a resumed node that
// reuses a checkpoint can reload its inputs instead of recomputing them.
package app

import (
	"bytes"
	"os"
	"path/filepath"

	"mediad/audio"
	"mediad/fingerprint"
	"mediad/internal/clock"
	"mediad/internal/sample"
	"mediad/manifest"
	"mediad/pipeline"
	"mediad/slice"
	"mediad/ts"
)

// Config controls one acceptance run.
type Config struct {
	OutDir         string
	SliceTicks     int64
	SliceTolerance int64
	WindowTicks    int64
	SilenceThresh  float64
	SilenceMin     int64
	TargetBitrate  int64
	InterruptAfter string
}

// DefaultConfig returns the deterministic demo parameters.
func DefaultConfig(out string) Config {
	return Config{
		OutDir:         out,
		SliceTicks:     clock.FromMs(5),
		SliceTolerance: clock.FromMs(1),
		WindowTicks:    clock.FromMs(5),
		SilenceThresh:  10,
		SilenceMin:     clock.FromMs(2),
		// Nominal TS bitrate: one 188-byte packet per millisecond.
		TargetBitrate: int64(ts.PacketSize) * 8 * 1000,
	}
}

// scratch is the in-memory hand-off between nodes within one process.
type scratch struct {
	demux    *ts.DemuxResult
	slices   []slice.Slice
	silences []audio.Interval
	printer  []fingerprint.Fingerprint
	manifest *manifest.Manifest
}

// Run executes (or resumes) the pipeline for one generated fixture kind.
func Run(kind sample.Kind, cfg Config) (*Report, error) {
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return nil, err
	}
	work := filepath.Join(cfg.OutDir, "work", string(kind))
	if err := os.MkdirAll(filepath.Join(work, "checkpoints"), 0o755); err != nil {
		return nil, err
	}
	store, err := pipeline.NewStore(filepath.Join(work, "checkpoints"))
	if err != nil {
		return nil, err
	}
	data, _ := sample.Generate(kind)
	inputHash := hashBytes(data)
	sc := &scratch{}

	nodes := buildNodes(kind, data, inputHash, cfg, work, sc)
	dag, err := pipeline.NewDAG(nodes)
	if err != nil {
		return nil, err
	}
	runner := pipeline.NewRunner(dag, store)
	if cfg.InterruptAfter != "" {
		runner.WithInterruptAfter(cfg.InterruptAfter)
	}
	results, err := runner.Execute()
	if err != nil {
		return &Report{Runner: runner, Fatal: err, Kind: kind}, err
	}
	// An interrupt leaves the DAG partially executed with no error: do not
	// fabricate a manifest, so the absence itself signals "not complete".
	if sc.manifest == nil && runner.Ran+runner.Reused == len(dag.Order()) {
		// A reused manifest node skipped Run; rebuild from persisted artifacts
		// so the report stays complete and identical to a fresh run.
		if _, err := buildManifest(kind, cfg, work, sc, nodes); err != nil {
			return nil, err
		}
	}
	return &Report{
		Runner: runner, Results: results, Kind: kind,
		Manifest: sc.manifest, Scratch: sc,
	}, nil
}

func buildNodes(kind sample.Kind, data []byte, inputHash string, cfg Config, work string, sc *scratch) []pipeline.Node {
	ingest := pipeline.Node{
		Name:   "ingest",
		Inputs: []string{inputHash},
		Params: map[string]any{"kind": string(kind)},
		Run: func(ctx *pipeline.RunContext) (string, error) {
			res, fatal := ts.Demux(bytes.NewReader(data), ts.ScanOptions{})
			if fatal != nil {
				return "", fatal // truncated/corrupt input -> exit code 2
			}
			sc.demux = res
			ctx.SetSourceOffsets([]pipeline.SourceOffset{{
				Input: string(kind), Start: 0, End: res.InputBytes,
			}})
			return writeArtifact(filepath.Join(work, "ingest.json"), res)
		},
	}
	analyze := pipeline.Node{
		Name:   "analyze",
		Deps:   []string{"ingest"},
		Inputs: []string{inputHash},
		Params: map[string]any{
			"slice_ticks": cfg.SliceTicks, "tolerance": cfg.SliceTolerance,
			"silence_min": cfg.SilenceMin, "silence_threshold": cfg.SilenceThresh,
		},
		Run: func(ctx *pipeline.RunContext) (string, error) {
			demux := sc.demux
			if demux == nil {
				if err := loadArtifact(filepath.Join(work, "ingest.json"), &demux); err != nil {
					return "", err
				}
			}
			slicer, _ := slice.New(slice.Config{
				TargetTicks: cfg.SliceTicks, ToleranceTicks: cfg.SliceTolerance,
				TargetBitrateBPS: cfg.TargetBitrate,
			})
			detector, _ := audio.NewDetector(audio.DetectorConfig{
				Threshold: cfg.SilenceThresh, MinDurationTicks: cfg.SilenceMin,
			})
			for _, ev := range demux.Events {
				slicer.Observe(ev, demux.Errors)
				detector.Observe(ev, demux.Errors)
			}
			sc.slices = slicer.Flush()
			sc.silences = detector.Flush()
			out := struct {
				Slices   []slice.Slice    `json:"slices"`
				Silences []audio.Interval `json:"silences"`
			}{sc.slices, sc.silences}
			return writeArtifact(filepath.Join(work, "analyze.json"), out)
		},
	}
	fpNode := pipeline.Node{
		Name:   "fingerprint",
		Deps:   []string{"analyze"},
		Inputs: []string{inputHash},
		Params: map[string]any{"window_ticks": cfg.WindowTicks},
		Run: func(ctx *pipeline.RunContext) (string, error) {
			demux := sc.demux
			if demux == nil {
				if err := loadArtifact(filepath.Join(work, "ingest.json"), &demux); err != nil {
					return "", err
				}
			}
			fp, err := fingerprint.NewFingerprinter(cfg.WindowTicks)
			if err != nil {
				return "", err
			}
			for _, ev := range demux.Events {
				fp.Add(ev)
			}
			sc.printer = fp.Flush()
			return writeArtifact(filepath.Join(work, "fingerprint.json"), sc.printer)
		},
	}
	allNodes := make([]pipeline.Node, 0, 4)
	manifestNode := pipeline.Node{
		Name:   "manifest",
		Deps:   []string{"fingerprint", "analyze"},
		Inputs: []string{inputHash},
		Params: map[string]any{"schema": pipeline.CodeVersion},
		Run: func(ctx *pipeline.RunContext) (string, error) {
			return buildManifest(kind, cfg, work, sc, allNodes)
		},
	}
	allNodes = append(allNodes, ingest, analyze, fpNode, manifestNode)
	return allNodes
}
