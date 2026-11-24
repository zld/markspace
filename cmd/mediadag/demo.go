package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"mediadag/internal/gen"
)

const (
	exitOK      = 0
	exitBlocked = 2 // expected: corrupt input or missing time anchor
	exitUsage   = 64
	exitUnexp   = 1
)

type demoArgs struct {
	seed          int64
	lossEvery     int
	dupEvery      int
	reorderWindow int
	targetKbps    float64
	segmentMs     float64
	checkpoint    string
	scenario      string
}

func parseDemoArgs(args []string) (*demoArgs, error) {
	d := &demoArgs{}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Int64Var(&d.seed, "seed", 41, "deterministic generator seed")
	fs.IntVar(&d.lossEvery, "loss-every", 0, "drop every Nth media packet (0=scenario default)")
	fs.IntVar(&d.dupEvery, "dup-every", 0, "duplicate every Nth media packet (0=scenario default)")
	fs.IntVar(&d.reorderWindow, "reorder-window", 0, "shuffle chunk size (0=scenario default)")
	fs.Float64Var(&d.targetKbps, "target-kbps", 1800, "target bitrate in kbps")
	fs.Float64Var(&d.segmentMs, "segment-ms", 2000, "manifest segment duration in ms")
	fs.StringVar(&d.checkpoint, "checkpoint", "",
		"checkpoint file path (empty keeps state in memory for the process)")
	fs.StringVar(&d.scenario, "scenario", "all",
		"sample scenario: all|clean|lossy|truncated|noanchor")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	switch d.scenario {
	case "all", "clean", "lossy", "truncated", "noanchor":
	default:
		return nil, fmt.Errorf("unknown scenario %q", d.scenario)
	}
	return d, nil
}

// lossyParams are the shared generator settings for the impaired samples.
func (d *demoArgs) lossyParams() gen.Params {
	p := gen.Params{
		Seed:          d.seed,
		Kbps:          int(d.targetKbps),
		LossEvery:     d.lossEvery,
		DupEvery:      d.dupEvery,
		ReorderWindow: d.reorderWindow,
	}
	if p.LossEvery == 0 {
		p.LossEvery = 37
	}
	if p.DupEvery == 0 {
		p.DupEvery = 23
	}
	if p.ReorderWindow == 0 {
		p.ReorderWindow = 12
	}
	return p
}

// cleanParams are the lossless baseline settings.
func (d *demoArgs) cleanParams() gen.Params {
	return gen.Params{Seed: d.seed, Kbps: int(d.targetKbps)}
}

// paramsJSONFor is the canonical parameter record embedded in the report.
func paramsJSONFor(sample *gen.Sample, p gen.Params, targetKbps, segmentMs float64) []byte {
	b, _ := json.Marshal(map[string]any{
		"input_digest":   sample.Digest,
		"target_kbps":    targetKbps,
		"segment_ms":     segmentMs,
		"loss_every":     p.LossEvery,
		"reorder_window": p.ReorderWindow,
		"dup_every":      p.DupEvery,
		"truncate":       p.Truncate,
		"no_rtcp":        p.NoRTCP,
		"versions": map[string]string{
			"mediadag":  nodeVersion,
			"go-mp4":    "v1.3.0",
			"pion/rtp":  "v1.8.11",
			"pion/rtcp": "v1.2.15",
		},
	})
	return b
}
