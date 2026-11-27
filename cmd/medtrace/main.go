// Command medtrace is the deterministic media-trace verification CLI.
//
// Usage:
//
//	medtrace demo   --seed N --out DIR [--corrupt MODE]
//	medtrace verify --input webm --events jsonl --state DIR
//	medtrace report --state DIR
//	medtrace resume --input webm --events jsonl --state DIR
//
// Exit codes: 0 success, 2 input/format error, 3 state corruption.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"medtrace/internal/fixture"
	"medtrace/internal/mederr"
	"medtrace/internal/pipeline"
	"medtrace/internal/state"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "demo":
		err = runDemo(args)
	case "verify":
		err = runVerify(args, false)
	case "resume":
		err = runVerify(args, true)
	case "report":
		err = runReport(args)
	default:
		usage()
		err = mederr.New(mederr.CatInvalidInput, "unknown command %q", cmd)
	}
	if err != nil {
		se := mederr.AsError(err)
		if se != nil {
			fmt.Fprintf(os.Stderr, "error category=%s offset=%d path=%v\n  %s\n",
				se.Category, se.Offset, se.Path, se.Message)
			if se.Cause != nil {
				fmt.Fprintf(os.Stderr, "  cause: %v\n", se.Cause)
			}
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(mederr.ExitCode(err))
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `medtrace - deterministic media trace verifier

commands:
  demo   --seed N --out DIR [--corrupt unknown-length|invalid-vint|cycle|loss-recovery]
  verify --input sample.webm --events events.jsonl --state DIR
  resume --input sample.webm --events events.jsonl --state DIR
  report --state DIR
`)
}

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 1, "deterministic generator seed")
	out := fs.String("out", "", "output directory")
	corrupt := fs.String("corrupt", "", "optional corruption mode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return mederr.New(mederr.CatInvalidInput, "demo requires --out")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return mederr.New(mederr.CatIO, "create output dir").Wrap(err)
	}

	mode := fixture.Corruption(*corrupt)
	webmCfg := fixture.DefaultWebMConfig(*seed)
	webmCfg.Corruption = mode
	webm, refs := fixture.GenerateWebM(webmCfg)
	evCfg := fixture.DefaultEventConfig(*seed)
	if mode == fixture.CorruptLossRecovery {
		// All gaps heal outside the window: recovery rate drops to 0.
		evCfg.Recovered = 0
	}
	events, truth := fixture.GenerateEvents(evCfg)

	webmPath := filepath.Join(*out, "sample.webm")
	eventsPath := filepath.Join(*out, "events.jsonl")
	if err := os.WriteFile(webmPath, webm, 0o644); err != nil {
		return mederr.New(mederr.CatIO, "write webm").Wrap(err)
	}
	if err := os.WriteFile(eventsPath, fixture.MarshalEvents(events), 0o644); err != nil {
		return mederr.New(mederr.CatIO, "write events").Wrap(err)
	}

	// Manifest records generator truth and any cycle descriptor.
	manifest := map[string]any{
		"seed": *seed, "corruption": *corrupt,
		"media_blocks": len(refs),
		"rtp_truth":    truth,
	}
	if mode == fixture.CorruptCycle {
		cycle := fixture.CycleTracks()
		manifest["cycle_tracks"] = cycle
		cycleRaw, err := json.MarshalIndent(map[string]any{"tracks": cycle}, "", "  ")
		if err != nil {
			return mederr.New(mederr.CatIO, "encode cycle spec").Wrap(err)
		}
		if err := os.WriteFile(filepath.Join(*out, "cycle.json"), append(cycleRaw, '\n'), 0o644); err != nil {
			return mederr.New(mederr.CatIO, "write cycle spec").Wrap(err)
		}
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(*out, "manifest.json"), append(raw, '\n'), 0o644); err != nil {
		return mederr.New(mederr.CatIO, "write manifest").Wrap(err)
	}
	fmt.Printf("demo written to %s\n  %s\n  %s\n  manifest.json (blocks=%d, gaps=%d)\n",
		*out, webmPath, eventsPath, len(refs), truth.Gaps)
	return nil
}

func runVerify(args []string, allowRecovery bool) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := fs.String("input", "", "path to sample.webm")
	events := fs.String("events", "", "path to events.jsonl")
	stateDir := fs.String("state", "", "checkpoint directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *events == "" || *stateDir == "" {
		return mederr.New(mederr.CatInvalidInput, "verify requires --input, --events and --state")
	}
	// A cycle descriptor (demo --corrupt cycle) is validated before any
	// expensive stage runs and produces no success state on failure.
	cyclePath := filepath.Join(filepath.Dir(*input), "cycle.json")
	spec, err := pipeline.LoadCycleSpec(cyclePath)
	if err != nil {
		return err
	}
	if spec != nil {
		if _, err := pipeline.VerifyCycle(spec); err != nil {
			return err
		}
	}
	runner := pipeline.NewRunner(*stateDir, pipeline.DefaultParams())
	report, err := runner.Run(*input, *events, allowRecovery)
	if err != nil {
		return err
	}
	if err := report.Validate(pipeline.DefaultParams().SyncToleranceMs); err != nil {
		return err
	}
	fmt.Print(report.String())
	fmt.Println()
	return nil
}

func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	stateDir := fs.String("state", "", "checkpoint directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *stateDir == "" {
		return mederr.New(mederr.CatInvalidInput, "report requires --state")
	}
	run, err := state.Begin(*stateDir, false)
	if err != nil {
		return err
	}
	inputs, stages := run.Snapshot()
	if len(stages) == 0 {
		return mederr.New(mederr.CatStateCorrupt, "no completed stages in %s", *stateDir)
	}
	out := map[string]any{
		"inputs":           inputs,
		"completed_stages": run.CompletedStages(),
		"stages":           stages,
		"input_hashes":     run.InputHashes(),
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return mederr.New(mederr.CatIO, "encode report").Wrap(err)
	}
	fmt.Println(string(raw))
	return nil
}
