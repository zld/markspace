// Command archive-lab archives HLS/DASH restorations: it indexes local
// manifests and segments, chooses a conversion configuration from quality
// constraints, streams the inputs and reports measured results as JSON.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"archive-lab/internal/fixture"
	"archive-lab/internal/media"
	"archive-lab/internal/quality"
	"archive-lab/internal/runner"
)

const usageText = `archive-lab - local HLS/DASH restoration lab

usage:
  archive-lab fixture --dir DIR
  archive-lab run --input MANIFEST [flags]
  archive-lab swap --from DIR --to DIR

run flags:
  --input PATH        HLS .m3u8 or DASH .mpd manifest (required)
  --target-bps N      target bitrate in bits/s (required)
  --max-bps N         hard bitrate cap in bits/s (required)
  --min-psnr DB       minimum PSNR in dB (required)
  --budget-ms N       processing budget in milliseconds (required)
  --root DIR          input root all paths must stay inside (default: manifest dir)
  --index PATH        index file (default: <root>/.archive-lab/index.json)
  --cache DIR         cache directory (default: <root>/.archive-lab/cache)
  --artifacts DIR     output directory (default: <root>/.archive-lab/out)
  --metadata PATH     metadata file to include in the scan (repeatable)
  --resume            persist progress and resume completed segments (default true)
  --dry-run           report the chosen configuration without streaming
  --cancel-after N    cancel after N segments, for resume testing

exit codes: 0 success, 1 usage error, 2 input or processing failure
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		code := media.ExitCode(err)
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(code)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return media.Usage("missing command")
	}
	switch args[0] {
	case "fixture":
		return cmdFixture(args[1:], stdout)
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "swap":
		return cmdSwap(args[1:], stdout)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return nil
	default:
		fmt.Fprint(stderr, usageText)
		return media.Usage("unknown command %q", args[0])
	}
}

func cmdFixture(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dir := fs.String("dir", ".tmp/cases", "directory to write fixtures into")
	if err := fs.Parse(args); err != nil {
		return media.Usage("fixture: %v", err)
	}
	written, err := fixture.Generate(*dir)
	if err != nil {
		return media.Wrap(media.StageScan, "fixture", err, "generate fixtures")
	}
	fmt.Fprintf(stdout, "wrote %d fixture files to %s\n", len(written), *dir)
	for _, w := range written {
		fmt.Fprintf(stdout, "  %-40s %8d  %s\n", w.Path, w.Bytes, w.SHA256[:12])
	}
	return nil
}

// stringList collects a repeatable flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func cmdRun(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var metadata stringList
	input := fs.String("input", "", "manifest path")
	target := fs.Int64("target-bps", 0, "target bitrate")
	maxBps := fs.Int64("max-bps", 0, "maximum bitrate")
	minPSNR := fs.Float64("min-psnr", 0, "minimum PSNR in dB")
	budget := fs.Int64("budget-ms", 0, "processing budget in ms")
	root := fs.String("root", "", "input root")
	indexPath := fs.String("index", "", "index path")
	cacheDir := fs.String("cache", "", "cache directory")
	artifactDir := fs.String("artifacts", "", "artifact directory")
	resume := fs.Bool("resume", true, "persist and honour segment progress for resumable runs")
	dryRun := fs.Bool("dry-run", false, "choose and report a configuration without streaming segments")
	cancelAfter := fs.Int("cancel-after", 0, "cancel after N completed segments (0 disables)")
	fs.Var(&metadata, "metadata", "metadata file to scan (repeatable)")
	if err := fs.Parse(args); err != nil {
		return media.Usage("run: %v", err)
	}
	if fs.NArg() > 0 {
		return media.Usage("run: unexpected arguments %v", fs.Args())
	}
	if *input == "" {
		return media.Usage("run: --input is required")
	}
	req := quality.Request{TargetBps: *target, MaxBps: *maxBps, MinPSNR: *minPSNR, BudgetMs: *budget}
	if err := req.Validate(); err != nil {
		return err
	}
	rootDir := *root
	if rootDir == "" {
		rootDir = filepath.Dir(*input)
	}
	base := filepath.Join(rootDir, ".archive-lab")
	if *indexPath == "" {
		*indexPath = filepath.Join(base, "index.json")
	}
	if *cacheDir == "" {
		*cacheDir = filepath.Join(base, "cache")
	}
	if *artifactDir == "" {
		*artifactDir = filepath.Join(base, "out")
	}
	for _, m := range metadata {
		if _, err := filepath.Abs(m); err != nil {
			return media.Wrap(media.StageScan, "args", err, "invalid metadata path").WithPath(m)
		}
	}

	r := &runner.Runner{Cache: &runner.Cache{Dir: *cacheDir}}
	progressPath := ""
	if *resume {
		progressPath = filepath.Join(base, "progress.json")
	}
	opts := runner.Options{
		InputPath:    *input,
		InputRoot:    rootDir,
		IndexPath:    *indexPath,
		CacheDir:     *cacheDir,
		ArtifactDir:  *artifactDir,
		Metadata:     metadata,
		Request:      req,
		ProgressPath: progressPath,
		DryRun:       *dryRun,
	}
	var cancel runner.CancelFunc
	if *cancelAfter > 0 {
		done := 0
		cancel = func() bool {
			if done >= *cancelAfter {
				return true
			}
			done++
			return false
		}
	}
	res := r.Run(opts, cancel)
	if err := res.Report.WriteJSON(stdout); err != nil {
		return media.Wrap(media.StageReport, "output", err, "write report")
	}
	if res.Error != nil {
		return res.Error
	}
	return nil
}

func cmdSwap(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("swap", flag.ContinueOnError)
	fs.SetOutput(stdout)
	from := fs.String("from", "", "source directory")
	to := fs.String("to", "", "destination directory")
	if err := fs.Parse(args); err != nil {
		return media.Usage("swap: %v", err)
	}
	if *from == "" || *to == "" {
		return media.Usage("swap: --from and --to are required")
	}
	entries, err := os.ReadDir(*from)
	if err != nil {
		return media.Wrap(media.StageScan, "swap", err, "read source").WithPath(*from)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(*from, e.Name()))
		if err != nil {
			return media.Wrap(media.StageScan, "swap", err, "read file").WithPath(e.Name())
		}
		dst := filepath.Join(*to, e.Name())
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return media.Wrap(media.StageScan, "swap", err, "write file").WithPath(dst)
		}
		n++
	}
	fmt.Fprintf(stdout, "swapped %d files from %s into %s\n", n, *from, *to)
	return nil
}

var (
	_ = errors.Is
	_ = time.Second
)
