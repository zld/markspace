package main

import (
	"os"

	"replaypipe/internal/fixtures"
)

func run() int {
	cfg, err := parseArgs(os.Args[1:])
	if err != nil {
		fatalf("%v", err)
	}
	r := newReporter(os.Stdout)
	if code := execute(r, cfg); code != exitOK {
		return code
	}
	return r.exitCode()
}

// execute runs the selected scenario against an already-parsed config. It
// exists so tests can drive every path without spawning a subprocess.
func execute(r *reporter, cfg *config) int {
	r.printf("replaypipe verify scenario=%s root=%s", cfg.scenario, cfg.root)
	r.printf("budget pre=%dns post=%dns min-width=%d min-fps=%.3f max-work=%d max-out=%d",
		cfg.pre, cfg.post, cfg.minWidth, cfg.minFPS, cfg.maxWorkBytes, cfg.maxOutBytes)
	set, err := fixtures.Generate(cfg.root)
	if err != nil {
		fatalf("generate fixtures: %v", err)
	}
	r.printf("fixtures generated: normal=%s envelope=%s", set.Normal, set.Envelope)
	switch cfg.scenario {
	case "normal":
		runNormal(r, cfg, set)
	case "envelope":
		runEnvelope(r, cfg, set)
	case "event":
		runEvent(r, cfg, set)
	case "corrupt":
		runCorrupt(r, cfg, set)
	case "migrate":
		runMigrate(r, cfg, set)
	default:
		runAll(r, cfg, set)
	}
	return r.exitCode()
}

func main() { os.Exit(run()) }
