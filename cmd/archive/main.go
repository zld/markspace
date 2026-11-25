// Command archive generates local archive fixtures, runs the reusable media
// kernel over them and prints a deterministic summary to the terminal. It does
// not touch the network, a graphics device or an external service.
//
// Exit codes:
//
//	0  success
//	2  input was damaged but partially recovered
//	1  fatal: an unusable timebase, an exceeded limit or a write failure
//	64 usage error
//
// The command only generates fixtures, calls the reusable packages and prints
// results.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"archive.example/mediakernel/internal/archive"
	"archive.example/mediakernel/internal/events"
	"archive.example/mediakernel/internal/fixture"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("archive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		kind       = fs.String("fixture", "normal", "input fixture: normal, truncated, damaged, late or boundary")
		seed       = fs.Int64("seed", 17, "deterministic seed for generated inputs")
		outRoot    = fs.String("out", "", "required output root; the manifest is written to <out>/manifest.json")
		lateWindow = fs.String("late-window", "2s", "allowed event lateness, e.g. 2s or 500ms")
		verbose    = fs.Bool("v", false, "print per-track and per-error detail")
	)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: archive -fixture normal -seed 17 -out /tmp/archive-a\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *outRoot == "" {
		fmt.Fprintln(stderr, "error: -out is required")
		fs.Usage()
		return 64
	}
	parsed, err := fixture.ParseKind(*kind)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 64
	}
	lateNS, err := parseDurationNS(*lateWindow)
	if err != nil {
		fmt.Fprintf(stderr, "error: -late-window: %v\n", err)
		return 64
	}

	cfg := archive.DefaultConfig(parsed, *seed)
	cfg.LateWindowNS = lateNS
	res, fatal := archive.Run(cfg, *outRoot)

	printReport(stdout, res.Report, *verbose)
	for _, line := range archive.SortedErrorLines(res.Report.Errors) {
		fmt.Fprintf(stdout, "  error %s\n", line)
	}
	if *verbose {
		printVerbose(stdout, res)
	}

	if fatal != nil {
		fmt.Fprintf(stderr, "fatal: %s\n", fatal.Error())
		return exitCodeFor(fatal)
	}
	if res.Report.Recovered {
		return 2
	}
	if !res.Report.FramesOK {
		fmt.Fprintln(stderr, "fatal: frame alignment exceeded one frame duration")
		return 1
	}
	if !res.Report.AudioFinite {
		fmt.Fprintln(stderr, "fatal: loudness peak was not finite")
		return 1
	}
	return 0
}

// exitCodeFor maps a fatal archive error to the documented exit code.
func exitCodeFor(e *events.Error) int {
	if e.Code == events.CodeFixture {
		return 64
	}
	// An unusable time base, an exceeded limit and a write/path failure are
	// all fatal with a non-zero, non-usage code.
	return 1
}

// parseDurationNS parses a compact duration such as "2s", "500ms" or "1500us".
func parseDurationNS(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	units := []struct {
		suffix string
		scale  int64
	}{
		{"ns", 1},
		{"us", 1_000},
		{"µs", 1_000},
		{"ms", 1_000_000},
		{"s", 1_000_000_000},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			num := strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			var v float64
			if _, err := fmt.Sscanf(num, "%g", &v); err != nil {
				return 0, fmt.Errorf("invalid number %q", num)
			}
			ns := int64(v * float64(u.scale))
			if ns < 0 {
				return 0, fmt.Errorf("negative duration")
			}
			return ns, nil
		}
	}
	return 0, fmt.Errorf("missing unit in %q (use ns, us, ms or s)", s)
}

// printVerbose prints per-track committed counts and the replay inventory.
func printVerbose(w io.Writer, res archive.RunResult) {
	if res.Document != nil {
		for _, t := range res.Document.Tracks {
			fmt.Fprintf(w, "  track %-9s id=%d handler=%q timescale=%d content=%t\n",
				t.Track, t.TrackID, t.Handler, t.Timescale, t.HasContent)
		}
	}
	byTrack := map[string]int{}
	for _, e := range res.Timeline.Committed() {
		byTrack[string(e.Track)]++
	}
	keys := make([]string, 0, len(byTrack))
	for k := range byTrack {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  committed %-9s %d\n", k, byTrack[k])
	}
}

func printReport(w io.Writer, r archive.Report, verbose bool) {
	fmt.Fprintf(w, "fixture=%s seed=%d\n", r.Fixture, r.Seed)
	for _, note := range r.Notes {
		fmt.Fprintf(w, "  note %s\n", note)
	}
	fmt.Fprintf(w, "frames=%d frame_duration_ns=%d\n", r.Frames, r.FrameDuration)
	fmt.Fprintf(w, "segments=%d\n", r.Segments)
	fmt.Fprintf(w, "pixels mean=%.3f min=%d max=%d\n", r.MeanByte, r.MinByte, r.MaxByte)
	fmt.Fprintf(w, "loudness peak=%.6f peak_dbfs=%.3f finite=%t\n", r.Peak, r.PeakDBFS, r.AudioFinite)
	fmt.Fprintf(w, "alignment max_error_ns=%d within_one_frame=%t\n", r.MaxAlignErr, r.FramesOK)
	fmt.Fprintf(w, "timeline committed=%d replays=%d late=%d watermark_ns=%d\n",
		r.Committed, r.Replays, r.Late, r.WatermarkNS)
	fmt.Fprintf(w, "rtp received=%d delivered=%d duplicates=%d out_of_order=%d lost=%d late=%d status=%s\n",
		r.RTPSummary.Received, r.RTPSummary.Delivered, r.RTPSummary.Duplicates,
		r.RTPSummary.OutOfOrder, r.RTPSummary.Lost, r.RTPSummary.Late, r.RTPSummary.Status)
	fmt.Fprintf(w, "gaps=%d errors=%d recovered=%t\n", r.GapCount, r.ErrorCount, r.Recovered)
	fmt.Fprintf(w, "sha256 state=%s\n", r.StateHash)
	fmt.Fprintf(w, "sha256 frames=%s\n", r.FramesHash)
	fmt.Fprintf(w, "sha256 manifest=%s\n", r.ManifestHash)
	if r.ManifestFileHash != "" {
		fmt.Fprintf(w, "sha256 manifest_file=%s\n", r.ManifestFileHash)
	}
	if r.ManifestPath != "" {
		fmt.Fprintf(w, "manifest path=%s\n", r.ManifestPath)
	}
}
