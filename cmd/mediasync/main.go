// Command mediasync is the acceptance adapter for the media processing core.
// It only orchestrates packages: parsing, timeline merge, validation and
// reporting all live in internal/*. Exit codes: 0 all rules pass, 1 material
// errors were found and reported, 2 the run itself failed.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"mediasync/internal/clock"
	"mediasync/internal/demo"
	"mediasync/internal/report"
	"mediasync/internal/validate"
)

const usage = `mediasync - offline media/subtitle alignment kernel

Usage:
  mediasync demo [--seed N] [--late-window D] [--json]
  mediasync check [--late-window D] --mp4 F [--srt F] [--vtt F] [--rtp F] [--timecode F]
  mediasync digest --mp4 F [--srt F] [--vtt F] [--rtp F] [--timecode F]

Flags:
  --seed        fixture seed for demo (default 7)
  --late-window late event window, e.g. 2s (default 2s)

Exit codes:
  0  all rules pass
  1  material errors were found; results were reported
  2  invalid parameters or execution failure
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "demo":
		return runDemo(rest, stdout, stderr)
	case "check":
		return runCheck(rest, stdout, stderr)
	case "digest":
		return runCheck(append(rest, "--digest-only"), stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "mediasync: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

func parseWindow(s string) (clock.Tick, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --late-window %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid --late-window %q: must not be negative", s)
	}
	return clock.Tick(d.Nanoseconds() * int64(clock.TickRate) / int64(time.Second)), nil
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 7, "fixture seed")
	window := fs.String("late-window", "2s", "late event window")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "mediasync demo: unexpected arguments %v\n", fs.Args())
		return 2
	}
	win, err := parseWindow(*window)
	if err != nil {
		fmt.Fprintf(stderr, "mediasync demo: %v\n", err)
		return 2
	}
	samples, err := demo.Run(*seed, win)
	if err != nil {
		fmt.Fprintf(stderr, "mediasync demo: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "mediasync demo seed=%d late-window=%s samples=%d\n", *seed, win, len(samples))
	exit := 0
	failed := 0
	for i, s := range samples {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprint(stdout, s.Report)
		if len(s.Result.Issues) > 0 {
			failed++
		}
	}
	// Cross-scenario invariants are part of acceptance.
	verdict, notes := verifyDemo(samples)
	for _, n := range notes {
		fmt.Fprintf(stdout, "\n%s\n", n)
	}
	if verdict != report.Pass {
		exit = 1
	}
	fmt.Fprintf(stdout, "\nsummary: samples=%d with_errors=%d digest=%s\n",
		len(samples), failed, combinedDigest(samples))
	return exit
}

// verifyDemo checks the documented acceptance relations between scenarios.
func verifyDemo(samples []demo.Sample) (report.Verdict, []string) {
	byName := map[string]demo.Sample{}
	for _, s := range samples {
		byName[s.Name] = s
	}
	var notes []string
	verdict := report.Pass
	fail := func(format string, a ...any) {
		verdict = report.DataErrors
		notes = append(notes, "ACCEPTANCE FAIL: "+fmt.Sprintf(format, a...))
	}
	pass := func(format string, a ...any) {
		notes = append(notes, "ACCEPTANCE OK: "+fmt.Sprintf(format, a...))
	}

	norm, ok := byName["normal"]
	if !ok {
		fail("normal sample missing")
	} else {
		if len(norm.Result.Issues) == 0 {
			pass("normal sample has no issues")
		} else {
			fail("normal sample reported %d issue(s), first: %s", len(norm.Result.Issues), norm.Result.Issues[0])
		}
		if norm.Result.MaxTimeDev <= 1 {
			pass("normal max time deviation %d tick(s) <= 1", int64(norm.Result.MaxTimeDev))
		} else {
			fail("normal max time deviation %d ticks > 1", int64(norm.Result.MaxTimeDev))
		}
	}

	if s, ok := byName["truncated-mp4"]; ok {
		if len(s.Result.Issues) > 0 && s.Result.First != nil && s.Result.First.Loc.ByteOffset >= 0 {
			pass("truncated MP4 reports first failure at byte %d with %d issue(s)",
				s.Result.First.Loc.ByteOffset, len(s.Result.Issues))
		} else {
			fail("truncated MP4 did not report a byte-localised failure")
		}
	} else {
		fail("truncated MP4 scenario missing")
	}

	if s, ok := byName["corrupt-rtp"]; ok {
		if len(s.Result.Issues) > 0 && s.Result.First != nil && s.Result.First.Loc.ByteOffset >= 0 {
			pass("corrupt RTP reports first failure at byte %d with %d issue(s)",
				s.Result.First.Loc.ByteOffset, len(s.Result.Issues))
		} else {
			fail("corrupt RTP did not report a byte-localised failure")
		}
	} else {
		fail("corrupt RTP scenario missing")
	}

	if s, ok := byName["illegal-df"]; ok {
		if len(s.Result.Issues) > 0 {
			pass("illegal DF timecode reported with source line %d", s.Result.Issues[0].Loc.Line)
		} else {
			fail("illegal DF timecode was accepted")
		}
	} else {
		fail("illegal DF scenario missing")
	}

	if s, ok := byName["invalid-srt"]; ok {
		cats := s.Result.Issues.Counts()
		if cats["invalid_time"] > 0 && cats["negative_duration"] > 0 && cats["overlapping_cue"] > 0 {
			pass("invalid SRT reports invalid time, negative duration and overlap with source lines")
		} else {
			fail("invalid SRT missing an expected category: %v", cats)
		}
	} else {
		fail("invalid SRT scenario missing")
	}

	if s, ok := byName["vfr-mp4"]; ok {
		if s.Result.VFR && len(s.Result.Intervals) > 1 &&
			s.Result.Intervals[0].DTSGap != s.Result.Intervals[2].DTSGap {
			pass("VFR keyframe intervals use actual time (%s vs %s)",
				s.Result.Intervals[0].DTSGap, s.Result.Intervals[2].DTSGap)
		} else {
			fail("VFR intervals were not derived from actual time")
		}
	} else {
		fail("VFR scenario missing")
	}

	if s, ok := byName["rtp-wrap"]; ok {
		if s.Result.RTPWraps == 1 && s.Result.RTPRegressions == 0 && len(s.Result.Issues) == 0 {
			pass("RTP 32-bit timestamp wrap expanded (%d packets, no spurious errors)", s.Result.PacketCount)
		} else {
			fail("RTP wrap not handled cleanly: wraps=%d regressions=%d issues=%d",
				s.Result.RTPWraps, s.Result.RTPRegressions, len(s.Result.Issues))
		}
	} else {
		fail("RTP wrap scenario missing")
	}

	inWindow, okIn := byName["late-in-window"]
	outWindow, okOut := byName["late-out-of-window"]
	if !okIn || !okOut {
		fail("late-event scenarios missing")
	} else {
		// The late scenario uses SRT+MP4 only, so its baseline is the same
		// analysis delivered in canonical order (demo.BaselineDigest).
		if inWindow.Result.Unrecoverable == 0 && inWindow.Digest == inWindow.BaselineDigest {
			pass("in-window late event recomputed to the event-time baseline digest")
		} else {
			fail("in-window late event digest %s != baseline %s (unrecoverable=%d)",
				inWindow.Digest, inWindow.BaselineDigest, inWindow.Result.Unrecoverable)
		}
		if outWindow.Result.Unrecoverable > 0 && outWindow.Result.First != nil {
			pass("out-of-window late event unrecoverable, first failure %s", outWindow.Result.First)
		} else {
			fail("out-of-window late event was not reported as unrecoverable")
		}
	}
	return verdict, notes
}

func combinedDigest(samples []demo.Sample) string {
	parts := make([]string, len(samples))
	for i, s := range samples {
		parts[i] = s.Name + "=" + s.Digest
	}
	return shortHash(strings.Join(parts, "\n"))
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	window := fs.String("late-window", "2s", "late event window")
	mp4Path := fs.String("mp4", "", "MP4 input")
	srtPath := fs.String("srt", "", "SRT subtitle input")
	vttPath := fs.String("vtt", "", "WebVTT subtitle input")
	rtpPath := fs.String("rtp", "", "RTP recording input")
	tcPath := fs.String("timecode", "", "timecode list input")
	digestOnly := fs.Bool("digest-only", false, "print only the SHA-256 digest")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *mp4Path == "" && *srtPath == "" && *vttPath == "" && *rtpPath == "" && *tcPath == "" {
		fmt.Fprintln(stderr, "mediasync check: at least one input is required")
		return 2
	}
	win, err := parseWindow(*window)
	if err != nil {
		fmt.Fprintf(stderr, "mediasync check: %v\n", err)
		return 2
	}
	res, err := validate.FromFiles(validate.Files{
		Name: "check", Window: win, MP4: *mp4Path, SRT: *srtPath,
		VTT: *vttPath, RTP: *rtpPath, Timecode: *tcPath,
	})
	if err != nil {
		fmt.Fprintf(stderr, "mediasync check: %v\n", err)
		return 2
	}
	if *digestOnly {
		fmt.Fprintln(stdout, report.Digest(report.BuildSnapshot(res)))
		return exitCode(res)
	}
	fmt.Fprint(stdout, report.Render(res))
	return exitCode(res)
}

func exitCode(res *validate.Result) int {
	if len(res.Issues) > 0 {
		return 1
	}
	return 0
}
