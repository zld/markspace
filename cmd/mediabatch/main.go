// Command mediabatch is the terminal entry point for the media batch pipeline:
// fixture generation, chunked publish, interruption recovery and report
// verification. It has no GUI dependency; every action is a subcommand plus
// flags, and results are printed to stdout.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediabatch/internal/batch"
	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
	"mediabatch/internal/fixture"
	"mediabatch/internal/scan"
)

const usage = `mediabatch - traceable DASH/HLS batch processing

Usage:
  mediabatch demo   --work DIR [--chunk N] [--inject-crash POINT] [--from T] [--to T]
  mediabatch resume --work DIR [--chunk N] [--from T] [--to T]
  mediabatch verify --work DIR

Flags:
  --work DIR            work directory (inputs live in DIR/input)
  --chunk N             segments per checkpoint chunk (default 3)
  --inject-crash POINT  after-artifact | after-commit
  --from T, --to T      restrict the report to an input time range (seconds or num/den)
  --input DIR           override the input root (default DIR/input)
  --max-segment-bytes N per-segment read limit (default 64 MiB)
  --regen               regenerate fixtures even if the input tree exists

Exit codes:
  0  success, including a completed report that lists content anomalies
  1  usage, I/O or index damage that prevents a report
  2  manifest parse failure
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 1
	}
	cmd := args[0]
	flags, err := parseFlags(args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: %v\n\n%s", err, usage)
		return 1
	}
	switch cmd {
	case "demo":
		return runDemo(flags)
	case "resume":
		return runResume(flags)
	case "verify":
		return runVerify(flags)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "mediabatch: unknown command %q\n\n%s", cmd, usage)
		return 1
	}
}

type flagSet struct {
	work     string
	input    string
	chunk    int
	chunkSet bool
	crash    string
	from     *clock.Time
	to       *clock.Time
	maxBytes int64
	regen    bool
}

func parseFlags(args []string) (*flagSet, error) {
	fs := &flagSet{chunk: 3, maxBytes: scan.DefaultMaxSegmentBytes}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		take := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", name)
			}
			i++
			return args[i], nil
		}
		switch name {
		case "--work", "-work":
			v, err := take()
			if err != nil {
				return nil, err
			}
			fs.work = v
		case "--input", "-input":
			v, err := take()
			if err != nil {
				return nil, err
			}
			fs.input = v
		case "--chunk", "-chunk":
			v, err := take()
			if err != nil {
				return nil, err
			}
			n, err := parseInt(v)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("--chunk must be a positive integer, got %q", v)
			}
			fs.chunk = n
			fs.chunkSet = true
		case "--inject-crash", "-inject-crash":
			v, err := take()
			if err != nil {
				return nil, err
			}
			if _, ok := fixture.ParseCrashPoint(v); !ok {
				return nil, fmt.Errorf("--inject-crash must be after-artifact or after-commit, got %q", v)
			}
			fs.crash = v
		case "--from", "-from":
			v, err := take()
			if err != nil {
				return nil, err
			}
			t, err := clock.FromString(v)
			if err != nil {
				return nil, fmt.Errorf("--from: %w", err)
			}
			fs.from = &t
		case "--to", "-to":
			v, err := take()
			if err != nil {
				return nil, err
			}
			t, err := clock.FromString(v)
			if err != nil {
				return nil, fmt.Errorf("--to: %w", err)
			}
			fs.to = &t
		case "--max-segment-bytes", "-max-segment-bytes":
			v, err := take()
			if err != nil {
				return nil, err
			}
			n, err := parseInt(v)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("--max-segment-bytes must be positive, got %q", v)
			}
			fs.maxBytes = int64(n)
		case "--regen", "-regen":
			fs.regen = true
		default:
			return nil, fmt.Errorf("unknown flag %q", arg)
		}
	}
	if fs.from != nil && fs.to != nil && fs.from.Cmp(*fs.to) > 0 {
		return nil, fmt.Errorf("--from %s is after --to %s", fs.from, fs.to)
	}
	return fs, nil
}

func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

func (f *flagSet) inputDir() string {
	if f.input != "" {
		return f.input
	}
	return filepath.Join(f.work, "input")
}

func (f *flagSet) requireWork() error {
	if f.work == "" {
		return errors.New("--work DIR is required")
	}
	return nil
}

func runDemo(f *flagSet) int {
	if err := f.requireWork(); err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: %v\n", err)
		return 1
	}
	layout, err := ensureFixtures(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: generate fixtures: %v\n", err)
		return 1
	}
	fmt.Printf("fixtures: %d media segments, manifests=%v\n", layout.Segments, layout.Manifest)

	res, err := batch.Execute(batch.Options{
		WorkDir: f.work, InputDir: f.inputDir(), ChunkSize: f.chunk,
		MaxSegmentBytes: f.maxBytes, CrashPoint: f.crash,
	}, batch.ModeDemo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: %v\n", describe(err))
		return exitCode(err)
	}
	if err := res.WriteReport(f.work, f.from, f.to); err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: write report: %v\n", err)
		return 1
	}
	printCounts(res)
	fmt.Printf("report: %s\n", filepath.Join(f.work, "report.txt"))
	if res.Crashed {
		fmt.Printf("crash-injected: stopped at %s after chunk %d; run 'resume' to continue\n",
			res.CrashPoint, res.Plan.ResumeChunk)
	}
	return reportExitCode(res)
}

// reportExitCode distinguishes a completed report that lists material
// anomalies (still exit 0, per the acceptance contract) from a manifest parse
// failure, which is a hard error.
func reportExitCode(res *batch.Run) int {
	if res.ManifestParseFailed() {
		return 2
	}
	return 0
}

func runResume(f *flagSet) int {
	if err := f.requireWork(); err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: %v\n", err)
		return 1
	}
	// A crash may occur before the first index commit, so an existing
	// checkpoint is enough to resume. Only a completely absent work dir is an
	// error.
	if _, err := os.Stat(filepath.Join(f.work, "checkpoint.json")); err != nil {
		if _, ierr := os.Stat(filepath.Join(f.work, "index.json")); ierr != nil {
			fmt.Fprintf(os.Stderr, "mediabatch: no checkpoint or index in %s; run demo first\n", f.work)
			return 1
		}
	}
	inspect, err := batch.Inspect(f.work, f.inputDir(), f.maxBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: %v\n", describe(err))
		return exitCode(err)
	}
	before := inspect.SummariseResume(f.work)
	fmt.Println(before.CheckpointLine)
	fmt.Printf("resume: plan=%d chunks, resume-chunk=%d\n", before.TotalChunks, before.ResumeChunk)

	res, err := batch.Execute(batch.Options{
		WorkDir: f.work, InputDir: f.inputDir(), ChunkSize: chunkOrDefault(f, before),
		MaxSegmentBytes: f.maxBytes,
	}, batch.ModeResume)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: %v\n", describe(err))
		return exitCode(err)
	}
	if err := res.WriteReport(f.work, f.from, f.to); err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: write report: %v\n", err)
		return 1
	}
	after := res.SummariseResume(f.work)
	// "reused" counts every artifact that recovery left in place, whether it
	// was already committed or adopted from disk; "adopted" is the subset that
	// had no index entry because the process died between the two writes.
	fmt.Printf("recovery: reused=%d (adopted=%d) recomputed=%d revalidated=%d new-artifacts=%d duplicate-publish=%d\n",
		after.Reused+after.Adopted, after.Adopted, after.Recomputed, after.Revalidated,
		after.NewArtifacts, after.DuplicatePublish)
	for _, cp := range after.Decisions {
		fmt.Printf("  chunk %d [%d..%d] %s", cp.Chunk, cp.Start, cp.End, cp.Decision)
		if cp.Committed {
			fmt.Printf(" committed")
		}
		if len(cp.Missing) > 0 {
			fmt.Printf(" missing=%d", len(cp.Missing))
		}
		if len(cp.Uncommitted) > 0 {
			fmt.Printf(" uncommitted=%d", len(cp.Uncommitted))
		}
		fmt.Println()
	}
	printCounts(res)
	return reportExitCode(res)
}

// chunkOrDefault keeps the chunk size the interrupted run used unless the
// caller explicitly overrides it; changing it mid-run would invalidate every
// chunk digest and force a full revalidation.
func chunkOrDefault(f *flagSet, s batch.ResumeSummary) int {
	if f.chunkSet {
		return f.chunk
	}
	if s.ChunkSize > 0 {
		return s.ChunkSize
	}
	return f.chunk
}

func runVerify(f *flagSet) int {
	if err := f.requireWork(); err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: %v\n", err)
		return 1
	}
	vr, err := batch.Verify(f.work)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: %v\n", describe(err))
		return exitCode(err)
	}
	fmt.Printf("verify: entries=%d sorted-violations=%d missing-source=%d index-entries=%d\n",
		vr.Entries, vr.SortedViolations, vr.MissingSource, vr.IndexEntries)
	fmt.Printf("manifest-digest=%s\n", vr.Digest)
	status := map[string]interface{}{
		"entries": vr.Entries, "sortedViolations": vr.SortedViolations,
		"missingSource": vr.MissingSource, "digest": vr.Digest, "indexEntries": vr.IndexEntries,
	}
	data, _ := json.MarshalIndent(status, "", "  ")
	if err := os.WriteFile(filepath.Join(f.work, "verify.json"), data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "mediabatch: write verify.json: %v\n", err)
		return 1
	}
	if vr.SortedViolations != 0 || vr.MissingSource != 0 {
		return 1
	}
	return 0
}

func ensureFixtures(f *flagSet) (*fixture.Layout, error) {
	manifestPath := filepath.Join(f.inputDir(), "dash", "manifest.mpd")
	if !f.regen {
		if _, err := os.Stat(manifestPath); err == nil {
			return fixture.DetectLayout(f.inputDir())
		}
	}
	return fixture.Generate(f.work, fixture.Options{Chunk: f.chunk})
}

func printCounts(res *batch.Run) {
	c := res.Counts
	fmt.Printf("summary: segments=%d anomalies=%d duplicates=%d chunks=%d published=%d new-artifacts=%d\n",
		c.Segments, c.Anomalies, c.Duplicates, c.Chunks, c.Published, c.NewArtifacts)
	if res.RecoveryIssues.Errors() > 0 {
		fmt.Printf("recovery: %d state anomalies recorded\n", res.RecoveryIssues.Errors())
	}
}

func describe(err error) string {
	var fe *fault.Err
	if errors.As(err, &fe) {
		return fe.Error()
	}
	return err.Error()
}

func exitCode(err error) int {
	var fe *fault.Err
	if errors.As(err, &fe) {
		switch fe.Code {
		case fault.CodeParse:
			return 2
		}
	}
	return 1
}
