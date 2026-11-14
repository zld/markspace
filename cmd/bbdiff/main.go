// Command bbdiff compares the basic blocks of two ELF firmware images.
//
// Usage:
//
//	bbdiff --old old.elf --new new.elf [--json] [flags]
//	bbdiff --generate-fixtures --old ./.work/old.elf --new ./.work/new.elf --json
//
// Only local files are read; bbdiff never opens a socket or contacts a remote
// service.  Generated fixtures are disposable and are cleaned up on request
// with --clean.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"bbdiff/internal/analyze"
	"bbdiff/internal/disasm"
	"bbdiff/internal/elfscan"
	"bbdiff/internal/fixture"
	"bbdiff/internal/report"
)

// exit codes
const (
	exportOK       = 0
	exportDiff     = 0 // differences are a normal report outcome
	exportUsage    = 2
	exportInvalid  = 3 // input is not a usable ELF sample
	exportAnalysis = 4 // analysis could not complete
	exportCancel   = 5
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type config struct {
	oldPath     string
	newPath     string
	jsonOut     bool
	generate    bool
	clean       bool
	workers     int
	timeout     time.Duration
	maxFileSize int64
	maxBlocks   int
	showDiag    bool
	showFuncs   bool
	quiet       bool
	version     bool
	newVariant  string
}

func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exportOK
		}
		fmt.Fprintf(stderr, "bbdiff: %v\n", err)
		return exportUsage
	}
	if cfg.version {
		fmt.Fprintf(stdout, "bbdiff: Capstone %d.%d\n", versionMajor(), versionMinor())
		fmt.Fprintf(stdout, "bbdiff: report json version %d\n", report.JSONVersion)
		return exportOK
	}
	if cfg.oldPath == "" || cfg.newPath == "" {
		fmt.Fprintln(stderr, "bbdiff: --old and --new are required")
		return exportUsage
	}

	ctx, stop := signalContext()
	defer stop()

	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
	}

	if cfg.generate {
		if err := generateFixtures(cfg, stderr); err != nil {
			fmt.Fprintf(stderr, "bbdiff: %v\n", err)
			return exportInvalid
		}
	}

	opt := analyze.DefaultOptions()
	opt.Context = ctx
	opt.Workers = cfg.workers
	opt.ELF.MaxFileSize = cfg.maxFileSize
	opt.MaxBlocksPerFunction = cfg.maxBlocks

	res, err := analyze.Run(cfg.oldPath, cfg.newPath, opt)
	if err != nil {
		fmt.Fprintf(stderr, "bbdiff: %v\n", err)
		if isELFProblem(err) {
			return exportInvalid
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return exportCancel
		}
		return exportAnalysis
	}

	if cfg.jsonOut {
		if err := report.WriteJSON(stdout, res); err != nil {
			fmt.Fprintf(stderr, "bbdiff: %v\n", err)
			return exportAnalysis
		}
	} else if !cfg.quiet {
		if err := report.WriteText(stdout, res, report.Options{
			ShowDiagnostics: cfg.showDiag,
			ShowFunctions:   cfg.showFuncs,
		}); err != nil {
			fmt.Fprintf(stderr, "bbdiff: %v\n", err)
			return exportAnalysis
		}
	}

	if cfg.clean {
		if err := cleanFixtures(cfg); err != nil {
			// Cleanup runs after reporting; always release what we can and
			// surface the failure without masking the analysis result.
			fmt.Fprintf(stderr, "bbdiff: cleanup: %v\n", err)
		}
	}
	if res.Cancelled {
		return exportCancel
	}
	if len(res.Failures) > 0 {
		return exportAnalysis
	}
	return exportOK
}

func parseFlags(args []string, stderr io.Writer) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("bbdiff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.oldPath, "old", "", "path to the old build ELF sample")
	fs.StringVar(&cfg.newPath, "new", "", "path to the new build ELF sample")
	fs.BoolVar(&cfg.jsonOut, "json", false, "emit JSON instead of the text report")
	fs.BoolVar(&cfg.generate, "generate-fixtures", false, "write deterministic demo fixtures to --old and --new before analysing")
	fs.BoolVar(&cfg.clean, "clean", false, "remove the generated fixture files after the run")
	fs.IntVar(&cfg.workers, "workers", 0, "concurrent analysis workers (0 = auto, max 8)")
	fs.DurationVar(&cfg.timeout, "timeout", 0, "abort the analysis after this duration (0 = no limit)")
	fs.Int64Var(&cfg.maxFileSize, "max-file-size", 512<<20, "maximum input file size in bytes")
	fs.IntVar(&cfg.maxBlocks, "max-blocks", analyze.DefaultOptions().MaxBlocksPerFunction, "maximum blocks retained per function")
	fs.BoolVar(&cfg.showDiag, "diagnostics", true, "print diagnostics in the text report")
	fs.BoolVar(&cfg.showFuncs, "functions", true, "print the per-function table in the text report")
	fs.BoolVar(&cfg.quiet, "quiet", false, "suppress the text report (useful with --json)")
	fs.BoolVar(&cfg.version, "version", false, "print linked Capstone version and exit")
	fs.StringVar(&cfg.newVariant, "new-variant", "new", "variant for the new fixture: new or corrupt (corrupt adds an unresolvable relocation to exercise the uncertain path)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: bbdiff --old OLD.elf --new NEW.elf [flags]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}
	if cfg.workers < 0 {
		return config{}, errors.New("--workers must be >= 0")
	}
	if cfg.maxFileSize <= 0 {
		return config{}, errors.New("--max-file-size must be positive")
	}
	if cfg.maxBlocks < 0 {
		return config{}, errors.New("--max-blocks must be >= 0")
	}
	if cfg.generate {
		if err := fixture.ValidateOutputPath(cfg.oldPath); err != nil {
			return config{}, err
		}
		if err := fixture.ValidateOutputPath(cfg.newPath); err != nil {
			return config{}, err
		}
		if samePath(cfg.oldPath, cfg.newPath) {
			return config{}, errors.New("--old and --new must be different paths")
		}
		if _, err := newVariant(cfg.newVariant); err != nil {
			return config{}, err
		}
	}
	return cfg, nil
}

func newVariant(name string) (fixture.Variant, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "new":
		return fixture.New, nil
	case "corrupt":
		return fixture.Corrupt, nil
	default:
		return 0, fmt.Errorf("unknown --new-variant %q (want new or corrupt)", name)
	}
}

func generateFixtures(cfg config, notice io.Writer) error {
	variant, err := newVariant(cfg.newVariant)
	if err != nil {
		return err
	}
	if err := fixture.Write(cfg.oldPath, fixture.Old); err != nil {
		return fmt.Errorf("generate old fixture: %w", err)
	}
	if err := fixture.Write(cfg.newPath, variant); err != nil {
		return fmt.Errorf("generate new fixture: %w", err)
	}
	if !cfg.quiet && !cfg.jsonOut {
		fmt.Fprintf(notice, "generated fixtures:\n  old: %s\n  new: %s\n\n", cfg.oldPath, cfg.newPath)
	}
	return nil
}

func cleanFixtures(cfg config) error {
	var errs []error
	for _, p := range []string{cfg.oldPath, cfg.newPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove %s: %w", filepath.Base(p), err))
		}
	}
	return errors.Join(errs...)
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return aa == bb
}

func isELFProblem(err error) bool {
	var readErr *elfscan.ReadError
	return errors.As(err, &readErr)
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func versionMajor() int { return disasm.SupportedVersion().Major }
func versionMinor() int { return disasm.SupportedVersion().Minor }
