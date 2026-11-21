// Command streamcheck builds and verifies HLS VOD media playlists from local
// media segments.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"streamcheck/internal/config"
	"streamcheck/internal/discover"
	"streamcheck/internal/fixture"
	"streamcheck/internal/manifest"
	"streamcheck/internal/probe"
	"streamcheck/internal/report"
	"streamcheck/internal/validate"
)

const topUsage = `streamcheck - build and verify HLS media playlists from local segments

Usage:
  streamcheck build   --input DIR --rates N[,N...] [options]
  streamcheck fixture --out DIR [options]

Exit codes:
  0  all checks passed
  1  media or manifest inconsistencies found
  2  parameter or runtime error
`

// Exit codes are part of the CLI contract.
const (
	exitOK           = 0
	exitInconsistent = 1
	exitUsage        = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, topUsage)
		return exitUsage
	}
	switch args[0] {
	case "build":
		return runBuild(args[1:], stdout, stderr)
	case "fixture":
		return runFixture(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, topUsage)
		return exitOK
	case "version", "--version":
		fmt.Fprintln(stdout, "streamcheck 1.0")
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], topUsage)
		return exitUsage
	}
}

func runFixture(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.ParseFixture(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	spec := fixture.Spec{
		Out:            cfg.Out,
		Rates:          cfg.Rates,
		Segments:       cfg.Segments,
		SegmentSeconds: cfg.SegmentSeconds,
		Pattern:        cfg.Pattern,
	}
	if err := fixture.Generate(spec); err != nil {
		fmt.Fprintf(stderr, "fixture: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "fixture written to %s\n", cfg.Out)
	fmt.Fprintf(stdout, "  rates: %v, %d segments per rate, %.3fs each\n",
		cfg.Rates, cfg.Segments, cfg.SegmentSeconds)
	fmt.Fprintf(stdout, "  cases: missing, duplicate, unordered, drift under %s/cases\n", cfg.Out)
	return exitOK
}

func runBuild(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.ParseBuild(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	pattern, err := manifest.CompilePattern(cfg.Pattern)
	if err != nil {
		fmt.Fprintf(stderr, "build: --pattern: %v\n", err)
		return exitUsage
	}
	if err := probe.Available(cfg.FFprobe); err != nil {
		fmt.Fprintf(stderr, "build: %v\n", err)
		return exitUsage
	}
	if err := ensureManifestDir(cfg.Input, cfg.ManifestDir); err != nil {
		fmt.Fprintf(stderr, "build: %v\n", err)
		return exitUsage
	}

	scan, err := discover.Scan(cfg.Input, discover.Options{Pattern: pattern, RateFilter: cfg.Rates})
	if err != nil {
		fmt.Fprintf(stderr, "build: %v\n", err)
		return exitUsage
	}
	if len(scan.Segments) == 0 && len(scan.Duplicates) == 0 {
		fmt.Fprintf(stderr, "build: no segments matching %q for rates %v in %s\n",
			cfg.Pattern, cfg.Rates, cfg.Input)
		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	probe.Run(ctx, scan.Segments, probe.Options{
		Binary:      cfg.FFprobe,
		Timeout:     cfg.ProbeTimeout,
		Concurrency: cfg.Concurrency,
	})
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "build: interrupted")
		return exitUsage
	}

	var warnings []string
	if scan.Ignored > 0 {
		warnings = append(warnings, fmt.Sprintf("ignored %d file(s) not matching %q", scan.Ignored, cfg.Pattern))
	}
	for _, rate := range scan.OtherRates {
		warnings = append(warnings, fmt.Sprintf("ignored segments for unrequested rate %d", rate))
	}

	checks := buildChecks(cfg, scan)
	validate.CompareRates(checks, cfg.ToleranceMS)

	// Detect files that changed between the scan/probe and now. This runs
	// before manifests are written so our own output never counts as change.
	globalIssues := validate.CheckUnchanged(scan.Files)

	digests := map[int]string{}
	for i := range checks {
		c := &checks[i]
		switch {
		case c.DiscoveredCount == 0:
			c.Issues = append(c.Issues, "no_segments_found")
		case c.SegmentCount == 0:
			c.Issues = append(c.Issues, "no_usable_segments")
		case len(c.DuplicateIDs) > 0:
			// Never emit a playlist with an ambiguous id set: the conflicting
			// ids are already reported as failures.
			c.Issues = append(c.Issues, "manifest_not_written_due_to_duplicate_ids")
			continue
		}
		path := filepath.Join(cfg.ManifestDir, fmt.Sprintf("media_%d.m3u8", c.Rate))
		c.ManifestPath = path
		playlist, err := manifest.BuildPlaylist(c.Rate, c.UsableSegments)
		if err != nil {
			globalIssues = append(globalIssues, fmt.Sprintf("rate %d: %v", c.Rate, err))
			continue
		}
		if err := manifest.WriteFileAtomic(path, manifest.Render(playlist)); err != nil {
			// A failed atomic write keeps any previous manifest intact; it is
			// a runtime error, not a media inconsistency.
			fmt.Fprintf(stderr, "build: write manifest %s: %v\n", filepath.Base(path), err)
			return exitUsage
		}
		digest, issues := validate.VerifyManifest(path,
			manifest.ExpectedSegments(c.Rate, c.UsableSegments), cfg.Input)
		digests[c.Rate] = digest
		c.Issues = append(c.Issues, issues...)
	}

	rep := report.Build(cfg, checks, globalIssues, warnings, digests)
	if cfg.JSON {
		if err := report.WriteJSON(stdout, rep); err != nil {
			fmt.Fprintf(stderr, "build: write report: %v\n", err)
			return exitUsage
		}
	} else {
		report.WriteText(stdout, rep)
	}
	return rep.ExitCode
}

// buildChecks analyzes each requested rate, mapping matches from a pattern
// without {rate} to the single requested rate.
func buildChecks(cfg *config.Build, scan discover.Result) []validate.RateCheck {
	checks := make([]validate.RateCheck, 0, len(cfg.Rates))
	for _, rate := range cfg.Rates {
		segments := make([]manifest.Segment, 0, len(scan.Segments))
		for _, seg := range scan.Segments {
			if seg.RateKbps == -1 {
				seg.RateKbps = rate
			}
			if seg.RateKbps == rate {
				segments = append(segments, seg)
			}
		}
		dups := scan.Duplicates[rate]
		if len(cfg.Rates) == 1 {
			if unkeyed, ok := scan.Duplicates[-1]; ok {
				dups = unkeyed
			}
		}
		checks = append(checks, validate.AnalyzeRate(rate, segments, dups, cfg.Strict))
	}
	return checks
}

// ensureManifestDir accepts the input directory or a direct child of it, so a
// build cannot write playlists elsewhere on the filesystem.
func ensureManifestDir(input, manifestDir string) error {
	in, err := filepath.Abs(input)
	if err != nil {
		return err
	}
	md, err := filepath.Abs(manifestDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(in, md)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside input directory %s", md, in)
	}
	if rel != "." && filepath.Dir(rel) != "." {
		return fmt.Errorf("%s must be %s or a direct subdirectory of it", md, in)
	}
	return nil
}
