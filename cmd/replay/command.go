package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"replay/internal/fault"
	"replay/internal/manifest"
	"replay/internal/mapasset"
	"replay/internal/replay"
	"replay/internal/scenario"
)

func printUsage(w io.Writer) {
	fmt.Fprint(w, `usage: replay <command> [options]

commands:
  demo    generate a scenario and run the replay pipeline
  verify  re-verify a published manifest
  help    show this message

demo options:
  --case <name>       scenario to build (default "normal")
  --versions <list>   comma-separated asset versions (default "v1,v2")
  --workdir <dir>     workspace for artefacts and checkpoints (default .replay/<case>)
  --stop-after <s>    halt after stage s: ingest|timeline|graph|map|merge|manifest
  --resume            continue from the first incomplete stage
  --quiet             suppress stage transitions

verify options:
  --workdir <dir>     workspace containing the manifest

exit codes: 0 manifest verified, 1 input or merge failure, 2 argument error
`)
}

// demoOptions holds parsed flags.
type demoOptions struct {
	runCase   string
	versions  string
	workDir   string
	stopAfter string
	resume    bool
	quiet     bool
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	opts, err := parseDemoFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "replay: %v\n", err)
		printUsage(stderr)
		return 2
	}
	versions, verr := mapasset.VersionsFromCSV(opts.versions)
	if verr != nil {
		return fail(stderr, verr)
	}
	if len(versions) == 0 {
		return fail(stderr, fault.New(fault.KindArgument, "--versions must not be empty").At("cli"))
	}
	fixture, ferr := scenario.Build(opts.runCase, versions)
	if ferr != nil {
		return fail(stderr, ferr)
	}

	workDir := opts.workDir
	if workDir == "" {
		workDir = filepath.Join(".replay", fixture.Case)
	}

	var progress io.Writer = stdout
	if opts.quiet {
		progress = io.Discard
	}
	engine := &replay.Engine{
		WorkDir:   workDir,
		Fixture:   fixture,
		StopAfter: opts.stopAfter,
		Resume:    opts.resume,
		Log:       progress,
	}
	result, runErr := engine.Run()
	if runErr != nil {
		printRunFailure(stdout, stderr, fixture, workDir, runErr)
		return fault.ExitCode(runErr)
	}

	printRunResult(stdout, fixture, result, workDir)
	if result.Outcome == replay.TargetStopped {
		return 0
	}
	if !result.Verified {
		return 1
	}
	return 0
}

func parseDemoFlags(args []string) (demoOptions, error) {
	var opts demoOptions
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.runCase, "case", scenario.CaseNormal, "scenario case")
	fs.StringVar(&opts.versions, "versions", "v1,v2", "comma-separated asset versions")
	fs.StringVar(&opts.workDir, "workdir", "", "workspace directory")
	fs.StringVar(&opts.stopAfter, "stop-after", "", "halt after this stage")
	fs.BoolVar(&opts.resume, "resume", false, "resume from first incomplete stage")
	fs.BoolVar(&opts.quiet, "quiet", false, "suppress stage transitions")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if opts.resume && opts.stopAfter != "" {
		return opts, fmt.Errorf("--resume and --stop-after are mutually exclusive")
	}
	return opts, nil
}

// printRunResult renders the terminal summary of a successful or stopped
// run.
func printRunResult(w io.Writer, fx scenario.Fixture, res *replay.Result, workDir string) {
	fmt.Fprintf(w, "case: %s\n", fx.Case)
	fmt.Fprintf(w, "workdir: %s\n", workDir)

	if res.Outcome == replay.TargetStopped {
		fmt.Fprintf(w, "status: stopped after %s\n", res.StoppedAfter)
		fmt.Fprintf(w, "resume position: %d %s\n", res.ResumePosition, res.ResumeUnit)
		printStages(w, res)
		return
	}

	if res.ResumedFrom != "" && len(res.Reused) > 0 {
		fmt.Fprintf(w, "resumed from: %s (reused %d stages)\n", res.ResumedFrom, len(res.Reused))
	}
	stats := res.Manifest.Stats
	fmt.Fprintf(w, "tracks: %d\n", stats.Tracks)
	fmt.Fprintf(w, "packets: %d\n", stats.Packets)
	fmt.Fprintf(w, "lost: %d\n", stats.Lost)
	fmt.Fprintf(w, "missing: %d\n", stats.Missing)
	fmt.Fprintf(w, "conflicts: %d\n", stats.Conflicts)
	fmt.Fprintf(w, "wraps: %d\n", stats.Wraps)
	fmt.Fprintf(w, "duplicates: %d\n", stats.DuplicateCount)
	fmt.Fprintf(w, "manifest sha256: %s\n", res.ManifestHash)
	fmt.Fprintf(w, "manifest verified: %v\n", res.Verified)
	fmt.Fprintf(w, "diff vs previous run: %d change(s)\n", res.Diff.Changes())
	if !res.Diff.Same {
		printDiff(w, res)
	}
	if len(res.Rollbacks) > 0 {
		fmt.Fprintf(w, "rollbacks: %d\n", len(res.Rollbacks))
		for _, rb := range res.Rollbacks {
			fmt.Fprintf(w, "  %-8s %s\n", rb.Stage, rb.Reason)
		}
	}
	printStages(w, res)
}

func printStages(w io.Writer, res *replay.Result) {
	fmt.Fprintln(w, "stages:")
	for _, cp := range res.Stages {
		fmt.Fprintf(w, "  %-8s %-11s recovered_at=%d %s\n", cp.Name, cp.Phase, cp.RecoveryPosition, cp.RecoveryUnit)
	}
}

func printDiff(w io.Writer, res *replay.Result) {
	if len(res.Diff.AddedPaths) > 0 {
		fmt.Fprintf(w, "  added: %s\n", strings.Join(res.Diff.AddedPaths, ", "))
	}
	if len(res.Diff.RemovedPaths) > 0 {
		fmt.Fprintf(w, "  removed: %s\n", strings.Join(res.Diff.RemovedPaths, ", "))
	}
	if len(res.Diff.ChangedPaths) > 0 {
		fmt.Fprintf(w, "  changed: %s\n", strings.Join(res.Diff.ChangedPaths, ", "))
	}
}

// printRunFailure reports a structured failure with its location.
func printRunFailure(stdout, stderr io.Writer, fx scenario.Fixture, workDir string, err error) {
	fmt.Fprintf(stdout, "case: %s\n", fx.Case)
	fmt.Fprintf(stdout, "workdir: %s\n", workDir)
	if fe, ok := fault.As(err); ok {
		fmt.Fprintf(stdout, "failed stage: %s\n", fe.Stage)
		if fe.Track != "" {
			fmt.Fprintf(stdout, "track: %s\n", fe.Track)
		}
		if fe.HasOffset {
			fmt.Fprintf(stdout, "byte offset: %d\n", fe.ByteOffset)
		}
		if fe.HasIndex {
			fmt.Fprintf(stdout, "input index: %d\n", fe.InputIndex)
		}
		if fe.HasSeq {
			fmt.Fprintf(stdout, "sequence: %d\n", fe.Seq)
		}
	}
	fmt.Fprintf(stderr, "replay: %v\n", err)
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "replay: %v\n", err)
	return fault.ExitCode(err)
}

// runVerify re-checks an existing manifest in a workspace.
func runVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workDir := fs.String("workdir", "", "workspace containing the manifest")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "replay: %v\n", err)
		return 2
	}
	if *workDir == "" {
		fmt.Fprintln(stderr, "replay: verify requires --workdir")
		return 2
	}
	return verifyWorkspace(stdout, stderr, *workDir)
}

func verifyWorkspace(stdout, stderr io.Writer, workDir string) int {
	doc, err := manifest.Load(manifestPath(workDir))
	if err != nil {
		return fail(stderr, err)
	}
	if _, err := manifest.Verify(workDir, doc); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "manifest sha256: %s\n", doc.ArtifactHash)
	fmt.Fprintf(stdout, "manifest verified: true\n")
	fmt.Fprintf(stdout, "entries: %d\n", len(doc.Entries))
	return 0
}

func manifestPath(workDir string) string { return filepath.Join(workDir, "manifest.json") }
