// Command archivist is the terminal entry point for the media repair kernel.
// It exposes a self-contained demo that generates its own fixtures, and a
// per-file mode that repairs arbitrary MP4/MOV inputs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"archivist/internal/fault"
	"archivist/internal/fixture"
	"archivist/internal/index"
	"archivist/internal/pipeline"
)

// Exit codes are part of the CLI contract.
const (
	exitOK      = 0
	exitFailure = 1
	exitCorrupt = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches subcommands and returns the process exit code.
func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage())
		return exitFailure
	}
	switch args[0] {
	case "demo":
		return cmdDemo(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "query":
		return cmdQuery(args[1:])
	case "check":
		return cmdCheck(args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage())
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "archivist: unknown command %q\n\n%s", args[0], usage())
		return exitFailure
	}
}

func usage() string {
	return `archivist repairs media timestamps and rebuilds random-access indexes.

Usage:
  archivist demo --dir <dir> [--queue N] [--chunk N]
      Generate fixtures, process them and print an acceptance report.
  archivist run --out <dir> [--queue N] [--chunk N] <file>...
      Repair the given MP4/MOV files.
  archivist check --out <dir> <file>
      Process one file and exit 0/2/1 per the acceptance contract.
  archivist query --index <index.json> --track N --time <ticks> [--movie]
      Resolve a time to the nearest sample and its keyframe.

Exit codes: 0 success, 2 corrupt input, 1 any other failure.
`
}

// report is the machine-readable summary printed on stdout.
type report struct {
	Command        string                  `json:"command"`
	Dir            string                  `json:"dir,omitempty"`
	Queue          int                     `json:"queue"`
	Chunk          int                     `json:"chunk,omitempty"`
	FirstRunMs     int64                   `json:"first_run_ms"`
	ResumeRunMs    int64                   `json:"resume_run_ms"`
	ResumeBudgetMs int64                   `json:"resume_budget_ms"`
	InterruptedAt  string                  `json:"interrupted_at,omitempty"`
	Results        []*pipeline.InputResult `json:"results"`
	Queries        []queryReport           `json:"queries,omitempty"`
	SampleFiles    []fixture.Sample        `json:"fixtures,omitempty"`
	Failure        string                  `json:"failure"`
}

// queryReport records one random-access lookup.
type queryReport struct {
	Sample        string `json:"sample"`
	TrackID       uint32 `json:"track_id"`
	Target        int64  `json:"target_ticks"`
	Keyframe      int    `json:"keyframe_sample"`
	KeyPTS        int64  `json:"keyframe_pts"`
	Delta         int64  `json:"delta_ticks"`
	KeyframeDelta int64  `json:"keyframe_delta_ticks"`
	Tolerance     int64  `json:"tolerance_ticks"`
	OK            bool   `json:"ok"`
}

func cmdDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "./tmp/demo", "directory for fixtures and work products")
	queue := fs.Int("queue", 4, "maximum inputs in flight")
	chunk := fs.Int("chunk", 1, "samples per chunk in rebuilt files")
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}
	if *queue <= 0 {
		fmt.Fprintln(os.Stderr, "archivist: --queue must be positive")
		return exitFailure
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "archivist: create %s: %v\n", *dir, err)
		return exitFailure
	}

	inputsDir := filepath.Join(*dir, "inputs")
	samples, err := fixture.BuildAll(inputsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivist: generate fixtures: %v\n", err)
		return exitFailure
	}
	manifestPath, err := fixture.WriteManifest(inputsDir, samples)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivist: write manifest: %v\n", err)
		return exitFailure
	}
	_ = manifestPath

	rep := &report{Command: "demo", Dir: *dir, Queue: *queue, Chunk: *chunk}
	for _, s := range samples {
		rep.SampleFiles = append(rep.SampleFiles, *s)
	}

	healthy := healthyInputs(samples)
	breakage := corruptInputs(samples)

	workDir := filepath.Join(*dir, "work")
	opts := pipeline.Options{
		QueueCapacity: *queue,
		WorkDir:       workDir,
		ChunkSamples:  *chunk,
	}

	// First pass: healthy inputs only. It establishes the reference output
	// digests and the wall-clock budget the resumed run must beat.
	firstStart := nowMs()
	first, err := runOnce(opts, healthy)
	rep.FirstRunMs = nowMs() - firstStart
	if err != nil {
		rep.Results = append(rep.Results, first...)
		return finish(rep, err)
	}
	rep.Results = append(rep.Results, first...)

	// Interrupted pass: a fresh work directory stops after the write stage.
	interruptedDir := filepath.Join(*dir, "work-interrupted")
	interrupted, interruptErr := runOnce(pipeline.Options{
		QueueCapacity:  *queue,
		WorkDir:        interruptedDir,
		ChunkSamples:   *chunk,
		InterruptAfter: 3,
	}, healthy)
	for _, r := range interrupted {
		rep.InterruptedAt = string(r.CheckpointStage)
		break
	}
	if interruptErr != nil {
		var stop *pipeline.ErrInterrupted
		if !errors.As(interruptErr, &stop) {
			rep.Results = append(rep.Results, interrupted...)
			return finish(rep, interruptErr)
		}
	}

	// Resumed pass: same directory, no interrupt. It must skip the completed
	// stages and reproduce the uninterruptible digests.
	resumeStart := nowMs()
	resumed, err := runOnce(pipeline.Options{
		QueueCapacity: *queue,
		WorkDir:       interruptedDir,
		ChunkSamples:  *chunk,
	}, healthy)
	rep.ResumeRunMs = nowMs() - resumeStart
	rep.ResumeBudgetMs = rep.FirstRunMs * 80 / 100
	if err != nil {
		rep.Results = append(rep.Results, resumed...)
		return finish(rep, err)
	}
	rep.Results = append(rep.Results, resumed...)

	if err := compareDigests(first, resumed); err != nil {
		return finish(rep, err)
	}
	if rep.ResumeRunMs > rep.ResumeBudgetMs {
		return finish(rep, fmt.Errorf(
			"resume took %dms, budget is 80%% of the %dms first run (%dms)",
			rep.ResumeRunMs, rep.FirstRunMs, rep.ResumeBudgetMs))
	}

	// Corrupt inputs must fail with an exact offset and exit code 2.
	corruptResults, corruptErr := runOnce(opts, breakage)
	rep.Results = append(rep.Results, corruptResults...)
	if corruptErr != nil && fault.ExitCode(corruptErr) != exitCorrupt {
		return finish(rep, corruptErr)
	}
	if err := checkCorruptResults(corruptResults); err != nil {
		return finish(rep, err)
	}

	// Random-access queries must stay within one sample tick of the target.
	queries, qerr := runQueries(first, samples)
	rep.Queries = queries
	if qerr != nil {
		return finish(rep, qerr)
	}
	if qerr := verifyQueryReports(queries); qerr != nil {
		return finish(rep, qerr)
	}

	// Idempotency: a rerun with the same inputs and parameters must produce
	// the same digests without redoing work.
	again, err := runOnce(opts, healthy)
	if err != nil {
		return finish(rep, err)
	}
	if err := compareDigests(first, again); err != nil {
		return finish(rep, err)
	}
	for _, r := range again {
		if len(r.ExecutedStages) != 0 {
			return finish(rep, fmt.Errorf("rerun of %s re-executed %v", r.Name, r.ExecutedStages))
		}
	}

	code := finish(rep, nil)
	if corruptErr != nil {
		// Corrupt samples are an expected part of the demo: report them, but
		// keep success exit code 0 because every check passed.
		code = exitOK
	}
	return code
}

// finish prints the report and maps an error to an exit code.
func finish(rep *report, err error) int {
	if err != nil {
		rep.Failure = err.Error()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if perr := enc.Encode(rep); perr != nil {
		fmt.Fprintf(os.Stderr, "archivist: encode report: %v\n", perr)
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivist: %v\n", err)
		return fault.ExitCode(err)
	}
	return exitOK
}

func runOnce(opts pipeline.Options, inputs []string) ([]*pipeline.InputResult, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	r, err := pipeline.New(opts)
	if err != nil {
		return nil, err
	}
	results, err := r.Run(context.Background(), inputs)
	if err != nil {
		return results, err
	}
	return results, nil
}

func healthyInputs(samples []*fixture.Sample) []string {
	var out []string
	for _, s := range samples {
		switch s.Kind {
		case fixture.KindTruncated, fixture.KindTruncatedHeader, fixture.KindBadLength:
			continue
		}
		out = append(out, s.Path)
	}
	return out
}

func corruptInputs(samples []*fixture.Sample) []string {
	var out []string
	for _, s := range samples {
		switch s.Kind {
		case fixture.KindTruncated, fixture.KindTruncatedHeader, fixture.KindBadLength:
			out = append(out, s.Path)
		}
	}
	return out
}

// compareDigests checks that two runs agree on every output and provenance
// digest for the same inputs.
func compareDigests(a, b []*pipeline.InputResult) error {
	if len(a) != len(b) {
		return fmt.Errorf("result count mismatch: %d vs %d", len(a), len(b))
	}
	index := make(map[string]*pipeline.InputResult, len(b))
	for _, r := range b {
		index[r.Name] = r
	}
	for _, r := range a {
		other, ok := index[r.Name]
		if !ok {
			return fmt.Errorf("input %s missing from second run", r.Name)
		}
		if r.OutputSHA256 != other.OutputSHA256 {
			return fmt.Errorf("output digest for %s changed: %s vs %s",
				r.Name, r.OutputSHA256, other.OutputSHA256)
		}
		if r.ProvenanceSHA256 != other.ProvenanceSHA256 {
			return fmt.Errorf("provenance digest for %s changed: %s vs %s",
				r.Name, r.ProvenanceSHA256, other.ProvenanceSHA256)
		}
	}
	return nil
}

// checkCorruptResults verifies every corrupt input reports the expected
// offset and never claims success.
func checkCorruptResults(results []*pipeline.InputResult) error {
	if len(results) == 0 {
		return nil
	}
	for _, r := range results {
		if r.Status == "ok" {
			return fmt.Errorf("corrupt input %s was marked ok", r.Name)
		}
		if r.ErrorOffset < 0 {
			return fmt.Errorf("corrupt input %s reported no byte offset: %s", r.Name, r.Error)
		}
		if r.OutputSHA256 != "" {
			return fmt.Errorf("corrupt input %s produced output", r.Name)
		}
	}
	return nil
}

func nowMs() int64 {
	return timeNow().UnixMilli()
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "./tmp/work", "output/work directory")
	queue := fs.Int("queue", 4, "maximum inputs in flight")
	chunk := fs.Int("chunk", 1, "samples per chunk in rebuilt files")
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}
	inputs := fs.Args()
	if len(inputs) == 0 {
		fmt.Fprintln(os.Stderr, "archivist: run requires at least one input file")
		return exitFailure
	}
	r, err := pipeline.New(pipeline.Options{
		QueueCapacity: *queue,
		WorkDir:       *out,
		ChunkSamples:  *chunk,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivist: %v\n", err)
		return fault.ExitCode(err)
	}
	results, err := r.Run(context.Background(), inputs)
	rep := &report{Command: "run", Dir: *out, Queue: *queue, Chunk: *chunk, Results: results}
	if err != nil {
		rep.Failure = err.Error()
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if perr := enc.Encode(rep); perr != nil {
		fmt.Fprintf(os.Stderr, "archivist: encode report: %v\n", perr)
		return exitFailure
	}
	for _, res := range results {
		if res.Status != "ok" {
			fmt.Fprintf(os.Stderr, "archivist: %s: %s\n", res.Name, res.Error)
		}
	}
	return fault.ExitCode(err)
}

func cmdQuery(args []string) int {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	indexPath := fs.String("index", "", "index JSON produced by run")
	track := fs.Uint("track", 0, "track ID")
	target := fs.Int64("time", 0, "target time in track ticks")
	movie := fs.Bool("movie", false, "treat --time as movie-timescale ticks")
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}
	if *indexPath == "" {
		fmt.Fprintln(os.Stderr, "archivist: --index is required")
		return exitFailure
	}
	ix, err := index.Load(*indexPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivist: %v\n", err)
		return fault.ExitCode(err)
	}
	var hit index.Hit
	if *movie {
		hit, err = ix.QueryMovie(uint32(*track), *target)
	} else {
		hit, err = ix.Query(uint32(*track), *target)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivist: %v\n", err)
		return fault.ExitCode(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(hit); err != nil {
		fmt.Fprintf(os.Stderr, "archivist: encode hit: %v\n", err)
		return exitFailure
	}
	return exitOK
}
