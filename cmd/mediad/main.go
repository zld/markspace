package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/example/mediad/internal/cache"
	"github.com/example/mediad/internal/fixture"
	"github.com/example/mediad/internal/pipeline"
	"github.com/example/mediad/internal/report"
)

var runStart = time.Now()

func elapsedMs() int64 { return time.Since(runStart).Milliseconds() }

func stderr(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func printJSON(s *report.Summary) {
	raw, err := s.JSON()
	if err != nil {
		stderr("encode summary: %v", err)
		return
	}
	fmt.Println(string(raw))
}

// buildInput converts fixture datagrams into a pipeline Input.
func buildInput(id, codec string, kind fixture.Kind, seed int64) pipeline.Input {
	dgs, err := fixture.Build(fixture.Spec{
		Seed: seed, Kind: kind, Codec: codec, Rate: 48000,
		PacketCount: 12, PayloadSize: 24,
	})
	if err != nil {
		panic(err)
	}
	in := pipeline.Input{
		MaterialID: id, CodecName: codec, SourceRate: 48000, Channels: 2,
	}
	for _, d := range dgs {
		in.Datagrams = append(in.Datagrams, struct {
			Data   []byte
			Offset int64
		}{Data: d.Data, Offset: d.Offset})
	}
	return in
}

// poisonAndRecover writes a cache entry with a deliberately wrong content
// digest, then runs the matching material. The runner's re-verification must
// reject the stale entry and recompute, proving self-healing.
func poisonAndRecover(runner *pipeline.Runner, c *cache.Cache, cfg demoConfig) (report.Item, bool) {
	in := buildInput("poisoned-opus", "opus", fixture.KindClean, cfg.seed+4)
	// Compute once to populate a real key and entry.
	first, err := runner.Run(in)
	if err != nil {
		return report.Item{}, false
	}
	spec := cache.Spec{
		SourceID:       in.MaterialID,
		InputDigest:    digestOf(in),
		Codec:          in.CodecName,
		SourceRate:     in.SourceRate,
		SourceChannels: uint8(in.Channels),
		TargetRate:     cfg.targetRate,
		Layout:         cfg.layout,
		LUFS:           cfg.lufs,
		TruePeak:       cfg.truePeak,
	}
	key := cache.Key(spec)
	// Overwrite with a poisoned digest; next Get must reject it.
	_ = c.Put(key, cache.Entry{
		Spec: spec, SourceDigest: spec.InputDigest,
		ContentDigest: "0000000000000000000000000000000000000000000000000000000000000000",
		Payload:       first.Artifact,
	})
	recovered, err := runner.Run(in)
	if err != nil {
		return report.Item{}, false
	}
	// Recomputed successfully after invalidating the poisoned entry.
	return recovered.Item, true
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitArgError)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(parseDemo(os.Args[2:])))
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		stderr("unknown command %q", os.Args[1])
		usage()
		os.Exit(exitArgError)
	}
}

func parseDemo(args []string) demoConfig {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	cfg := demoConfig{}
	fs.Int64Var(&cfg.seed, "seed", 42, "random seed for synthetic data")
	fs.Int64Var(&cfg.budgetMs, "budget-ms", 5000, "processing time budget in ms")
	fs.Int64Var(&cfg.maxBytes, "max-bytes", 65536, "max output bytes per material")
	rate := fs.Uint("target-rate", 48000, "target sample rate in Hz")
	fs.StringVar(&cfg.layout, "layout", "stereo", "target channel layout")
	fs.Float64Var(&cfg.lufs, "lufs", -16, "integrated loudness target in LUFS")
	fs.Float64Var(&cfg.truePeak, "true-peak", -1.0, "true-peak ceiling in dBTP")
	fs.StringVar(&cfg.workDir, "work-dir", "", "working directory for cache/checkpoints")
	if err := fs.Parse(args); err != nil {
		os.Exit(exitArgError)
	}
	cfg.targetRate = uint32(*rate)
	if !validLayout(cfg.layout) {
		stderr("invalid layout %q (want mono|stereo)", cfg.layout)
		os.Exit(exitArgError)
	}
	return cfg
}

func validLayout(s string) bool {
	return s == "mono" || s == "stereo"
}

func usage() {
	fmt.Fprint(os.Stderr,
		"mediad - local audio batch processing kernel\n\n"+
			"usage: mediad demo [flags]\n\n"+
			"flags:\n"+
			"  --seed int        random seed (default 42)\n"+
			"  --budget-ms int   time budget in ms (default 5000)\n"+
			"  --max-bytes int   output byte budget (default 65536)\n"+
			"  --target-rate int target sample rate Hz (default 48000)\n"+
			"  --layout string   mono|stereo (default stereo)\n"+
			"  --lufs float      loudness target LUFS (default -16)\n"+
			"  --true-peak float true-peak ceiling dBTP (default -1.0)\n"+
			"  --work-dir string working directory\n")
}

// digestOf mirrors the pipeline's input digest so the CLI can address cache.
func digestOf(in pipeline.Input) string {
	order, err := orderInput(in)
	if err != nil {
		return ""
	}
	return rtpDigestHex(order)
}
