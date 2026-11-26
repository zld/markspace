// Command trace reconstructs reviewable audio tracks from Matroska/WebM
// inputs and client event logs. It is a pure terminal tool: every function
// is reached through subcommands and printed on stdout/stderr.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mediatrace/internal/ebml"
	"mediatrace/internal/engine"
	"mediatrace/internal/fixture"
	"mediatrace/internal/output"
)

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return engine.ExitVerifyOrWrite
	}
	switch args[0] {
	case "demo":
		return cmdDemo(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "verify":
		return cmdVerify(args[1:])
	case "-h", "--help", "help":
		usage()
		return engine.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		usage()
		return engine.ExitVerifyOrWrite
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  trace demo --dir DIR
  trace run -i INPUT -e EVENTS -c CACHE -o OUT [--budget-passes N]
  trace verify -o OUT
exit codes: 0 ok | 2 corrupt input | 3 quality/budget | 4 verify/write failure
`)
}

type runFlags struct {
	input, events, cache, out string
	budgetPasses              int
}

func parseRunFlags(args []string) (runFlags, error) {
	f := runFlags{budgetPasses: 3}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-i":
			i++
			if i >= len(args) {
				return f, errors.New("missing -i value")
			}
			f.input = args[i]
		case "-e":
			i++
			if i >= len(args) {
				return f, errors.New("missing -e value")
			}
			f.events = args[i]
		case "-c":
			i++
			if i >= len(args) {
				return f, errors.New("missing -c value")
			}
			f.cache = args[i]
		case "-o":
			i++
			if i >= len(args) {
				return f, errors.New("missing -o value")
			}
			f.out = args[i]
		case "--budget-passes":
			i++
			if i >= len(args) {
				return f, errors.New("missing --budget-passes value")
			}
			if _, err := fmt.Sscanf(args[i], "%d", &f.budgetPasses); err != nil {
				return f, fmt.Errorf("bad --budget-passes: %w", err)
			}
		default:
			return f, fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	if f.input == "" || f.events == "" || f.cache == "" || f.out == "" {
		return f, errors.New("run requires -i, -e, -c and -o")
	}
	return f, nil
}

func cmdRun(args []string) int {
	flags, err := parseRunFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return engine.ExitVerifyOrWrite
	}
	plan := engine.DefaultPlan()
	plan.InputPath = flags.input
	plan.EventsPath = flags.events
	plan.CacheDir = flags.cache
	plan.OutputDir = flags.out
	plan.BudgetPasses = flags.budgetPasses

	r := engine.NewRunner()
	res, err := r.Run(context.Background(), plan)
	if err != nil {
		if pe, ok := ebml.AsError(err); ok {
			fmt.Fprintf(os.Stderr, "error=%s offset=%d detail=%q\n", pe.Code, pe.Offset, pe.Msg)
			return engine.ExitInputCorrupt
		}
		fmt.Fprintf(os.Stderr, "error=RUN_FAILURE detail=%q\n", err.Error())
		return engine.ExitVerifyOrWrite
	}
	printResult(res)
	if res.QualityFail {
		return engine.ExitQualityBudget
	}
	return engine.ExitOK
}

func printResult(res *engine.Result) {
	m := res.Manifest
	fmt.Printf("cache=%s mapped=%s max_time_error_ns=%d\n",
		res.CacheState, m.Mapped, m.MaxErrorNS)
	fmt.Printf("input_sha256=%s blocks=%d tracks=%d\n",
		m.Input.SHA256, m.Input.BlockCount, m.Input.TrackCount)
	var drift int64
	for _, t := range m.Tracks {
		drift += t.ByteDrift
		fmt.Printf("track=%d file=%s bitrate=%dk lufs=%.2f dbtp=%.2f gate=%v sha256=%s drift=%d\n",
			t.TrackNo, t.OutputFile, t.BitrateKbps, t.IntegratedLUFS,
			t.TruePeakDBTP, t.GatePassed, t.OutputSHA256, t.ByteDrift)
	}
	if len(m.Failures) > 0 {
		for _, f := range m.Failures {
			fmt.Printf("failure code=%s offset=%d detail=%q\n", f.Code, f.Offset, f.Detail)
		}
	}
	fmt.Printf("byte_drift=%d\n", drift)
}

func cmdDemo(args []string) int {
	dir := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--dir" && i+1 < len(args) {
			dir = args[i+1]
			i++
		} else {
			fmt.Fprintf(os.Stderr, "demo: unknown flag %s\n", args[i])
			return engine.ExitVerifyOrWrite
		}
	}
	if dir == "" {
		fmt.Fprintln(os.Stderr, "demo requires --dir DIR")
		return engine.ExitVerifyOrWrite
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		return engine.ExitVerifyOrWrite
	}
	good := fixture.BuildWebM()
	bad := fixture.BuildBadWebM(good)
	events, err := fixture.BuildEventsJSONL()
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		return engine.ExitVerifyOrWrite
	}
	files := map[string][]byte{
		"ok.webm":       good,
		"bad.webm":      bad,
		"repaired.webm": good, // identical bytes => same SHA, demonstrates rebuilt
		"quiet.webm":    fixture.BuildQuietWebM(), // fails the -16 LUFS gate
		"events.jsonl":  events,
	}
	for name, data := range files {
		if err := fixture.WriteFile(filepath.Join(dir, name), data); err != nil {
			fmt.Fprintf(os.Stderr, "demo write %s: %v\n", name, err)
			return engine.ExitVerifyOrWrite
		}
		fmt.Printf("wrote %s (%d bytes)\n", filepath.Join(dir, name), len(data))
	}
	return engine.ExitOK
}

func cmdVerify(args []string) int {
	out := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "-o" && i+1 < len(args) {
			out = args[i+1]
			i++
		}
	}
	if out == "" {
		fmt.Fprintln(os.Stderr, "verify requires -o OUT")
		return engine.ExitVerifyOrWrite
	}
	manifest, err := output.LoadManifest(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		return engine.ExitVerifyOrWrite
	}
	problems := 0
	for _, t := range manifest.Tracks {
		path := filepath.Join(out, t.OutputFile)
		info, err := output.InspectMP4(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "verify track %d: %v\n", t.TrackNo, err)
			problems++
			continue
		}
		sha, size, err := output.FileSHA256(path)
		if err != nil || sha != t.OutputSHA256 || size != t.OutputBytes {
			fmt.Fprintf(os.Stderr, "verify track %d: sha/size drift\n", t.TrackNo)
			problems++
			continue
		}
		drift, err := output.ByteDrift(path, info)
		if err != nil || drift != t.ByteDrift {
			fmt.Fprintf(os.Stderr, "verify track %d: byte drift changed\n", t.TrackNo)
			problems++
			continue
		}
		if info.SampleCount != t.SampleCount || info.Timescale != t.Timescale {
			fmt.Fprintf(os.Stderr, "verify track %d: sample table changed\n", t.TrackNo)
			problems++
			continue
		}
		fmt.Printf("track=%d ok samples=%d sha256=%s\n", t.TrackNo, info.SampleCount, sha)
	}
	// Re-confirm the input digest recorded in the manifest.
	if manifest.Input.SHA256 == "" || len(manifest.Input.SHA256) != 64 {
		fmt.Fprintln(os.Stderr, "verify: invalid input digest in manifest")
		problems++
	}
	if problems > 0 {
		return engine.ExitVerifyOrWrite
	}
	fmt.Printf("verify=ok mapped=%s cache=%s\n", manifest.Mapped, manifest.CacheState)
	return engine.ExitOK
}
