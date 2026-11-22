// mediatool is the demo and acceptance entry point for the media pipeline
// library. It generates local faulty media samples, runs the full
// manifest -> ingest -> rtp -> audio -> merge pipeline, and prints metrics.
//
// Usage:
//
//	mediatool demo   --seed 23 --tracks 4 --workers 3 --faults all
//	mediatool verify --seed 23 --runs 2
//
// Exit codes: 0 = acceptance passed, 1 = metric/invariant failure,
// 2 = invalid arguments or input.
package main

import (
	"fmt"
	"os"
	"strconv"
)

const (
	exitOK       = 0
	exitFailed   = 1
	exitBadInput = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) < 1 {
		usage()
		return exitBadInput
	}
	switch args[0] {
	case "demo":
		cfg, code := parseFlags(args[1:], false)
		if code != exitOK {
			return code
		}
		return demo(cfg)
	case "verify":
		cfg, code := parseFlags(args[1:], true)
		if code != exitOK {
			return code
		}
		return verify(cfg)
	default:
		usage()
		return exitBadInput
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  mediatool demo   --seed N --tracks N --workers N --faults all|none
  mediatool verify --seed N --runs N`)
}

type cliConfig struct {
	Seed    int64
	Tracks  int
	Workers int
	Faults  string
	Runs    int
}

// parseFlags parses --key value pairs; verify mode defaults tracks/workers.
func parseFlags(args []string, verifyMode bool) (cliConfig, int) {
	cfg := cliConfig{Seed: 1, Tracks: 4, Workers: 3, Faults: "all", Runs: 2}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			fmt.Fprintf(os.Stderr, "missing value for %s\n", args[i])
			return cfg, exitBadInput
		}
		key, val := args[i], args[i+1]
		i++
		seen[key] = true
		num, err := strconv.ParseInt(val, 10, 64)
		switch key {
		case "--seed":
			if err != nil {
				return cfg, badf("bad --seed %q", val)
			}
			cfg.Seed = num
		case "--tracks":
			if err != nil || num < 1 || num > 64 {
				return cfg, badf("--tracks must be 1..64, got %q", val)
			}
			cfg.Tracks = int(num)
		case "--workers":
			if err != nil || num < 1 || num > 8 {
				return cfg, badf("--workers must be 1..8, got %q", val)
			}
			cfg.Workers = int(num)
		case "--runs":
			if err != nil || num < 1 || num > 16 {
				return cfg, badf("--runs must be 1..16, got %q", val)
			}
			cfg.Runs = int(num)
		case "--faults":
			if val != "all" && val != "none" {
				return cfg, badf("--faults must be all|none, got %q", val)
			}
			cfg.Faults = val
		default:
			return cfg, badf("unknown flag %s", key)
		}
	}
	if !verifyMode && !seen["--seed"] {
		return cfg, badf("demo requires --seed")
	}
	return cfg, exitOK
}

func badf(format string, a ...interface{}) int {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	return exitBadInput
}
