// Command mediapack is the terminal acceptance entry point. It generates
// self-made TS samples, runs the crash-safe multi-bitrate pack, recovers local
// failures and verifies roundtrip/keyframe/hash metrics.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"mediapack/internal/commit"
	merr "mediapack/internal/errors"
	"mediapack/internal/recover"
)

func main() {
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr))
}

func execute(args []string, out, errOut io.Writer) int {
	if len(args) < 1 {
		usage(errOut)
		return 1
	}
	switch args[0] {
	case "demo":
		return cmdDemo(args[1:], out, errOut)
	case "recover":
		return cmdRecover(args[1:], out, errOut)
	case "verify":
		return cmdVerify(args[1:], out, errOut)
	case "corrupt":
		return cmdCorrupt(args[1:], out, errOut)
	case "-h", "--help", "help":
		usage(out)
		return 0
	default:
		fmt.Fprintf(errOut, "unknown subcommand %q\n", args[0])
		usage(errOut)
		return 1
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: mediapack <demo|recover|verify|corrupt> [flags]")
}

func parseWork(name string, args []string, errOut io.Writer) (string, *flag.FlagSet, int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	work := fs.String("work", "", "work directory (local path)")
	if err := fs.Parse(args); err != nil {
		return "", fs, 2
	}
	return *work, fs, -1
}

func cmdDemo(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(errOut)
	work := fs.String("work", "", "work directory")
	rates := fs.Int("rates", 2, "number of bitrate representations")
	crash := fs.Bool("crash-before-commit", false, "simulate exit after write+prepare, before commit")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if strings.TrimSpace(*work) == "" {
		fmt.Fprintln(errOut, "--work is required")
		return 1
	}
	_, err := recover.Demo(recover.Options{WorkDir: *work, Rates: *rates, CrashBeforeCommit: *crash})
	if err != nil {
		if errors.Is(err, commit.ErrSimulatedCrash) {
			// Machine marker on stdout: `go run` normalizes a child's nonzero
			// status to 1, so surface the intended recovery-only code too.
			fmt.Fprintf(out, "simulated_crash=true exit_code=%d prepared_uncommitted=1\n", commit.CrashExitCode)
			fmt.Fprintln(errOut, "SIMULATED CRASH (exit 20): fragment written+prepared, status not committed")
			return commit.CrashExitCode
		}
		fmt.Fprintf(errOut, "demo failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "demo complete: work=%s rates=%d\n", *work, *rates)
	return 0
}

func cmdRecover(args []string, out, errOut io.Writer) int {
	work, _, code := parseWork("recover", args, errOut)
	if code >= 0 {
		return code
	}
	if work == "" {
		fmt.Fprintln(errOut, "--work is required")
		return 1
	}
	res, _, err := recover.Recover(work)
	if err != nil {
		fmt.Fprintf(errOut, "recover failed: %v\n", err)
		return 1
	}
	// Canonical machine line (stdout only): the acceptance token.
	fmt.Fprintf(out, "recovered=%d duplicate_publish=%d missing_artifact=%d\n",
		res.Recovered, res.DuplicatePublish, res.MissingArtifact)
	fmt.Fprintf(errOut, "detail rebuilt=%d adopted_temp=%d newly_published=%d already_consistent=%d total=%d\n",
		res.Rebuilt, res.AdoptedTemp, res.NewlyPublished, res.AlreadyConsistent, res.TotalArtifacts)
	if res.MissingArtifact != 0 {
		return 1
	}
	return 0
}

func cmdVerify(args []string, out, errOut io.Writer) int {
	work, _, code := parseWork("verify", args, errOut)
	if code >= 0 {
		return code
	}
	if work == "" {
		fmt.Fprintln(errOut, "--work is required")
		return 1
	}
	rep, m, err := recover.Verify(work)
	if err != nil {
		fmt.Fprintf(errOut, "verify failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "roundtrip_rate=%.3f (%d/%d)\n", rep.RoundtripRate, rep.RoundtripOK, rep.RoundtripTotal)
	fmt.Fprintf(out, "keyframe_hit_rate=%.3f (%d/%d)\n", rep.KeyframeRate, rep.KeyHits, rep.KeyTotal)
	fmt.Fprintf(out, "segment_hash_ok=%d/%d\n", rep.HashOK, rep.HashTotal)
	fmt.Fprintf(out, "fragment_struct_ok=%d/%d\n", rep.FragmentChecksOK, rep.FragmentTotal)
	fmt.Fprintf(out, "boundary_align_error_packets=%d\n", rep.BoundaryPackets)
	fmt.Fprintf(out, "boundary_alignment=%s max_error=%.3f limit_packets=1\n",
		passWord(rep.BoundaryPackets <= 1 && rep.Aligned), float64(rep.BoundaryPackets))
	fmt.Fprintf(out, "fanin_aligned=%t\n", rep.Aligned)

	cats := make([]string, 0, len(rep.Counts))
	var total int
	for c, n := range rep.Counts {
		cats = append(cats, string(c))
		total += n
	}
	sort.Strings(cats)
	summary := strings.Builder{}
	fmt.Fprintf(&summary, "anomaly_counts total=%d", total)
	for _, c := range cats {
		fmt.Fprintf(&summary, " %s=%d", c, rep.Counts[merr.Category(c)])
	}
	fmt.Fprintln(out, summary.String())
	for _, c := range cats {
		fmt.Fprintf(out, "anomaly_category category=%s count=%d\n", c, rep.Counts[merr.Category(c)])
	}
	if m != nil {
		fmt.Fprintf(out, "manifest_format_version=%d artifacts=%d\n", m.FormatVersion, len(m.Artifacts))
	}
	if rep.Pass {
		fmt.Fprintln(out, "verify=PASS")
		return 0
	}
	fmt.Fprintln(out, "verify=FAIL")
	return 1
}

func passWord(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}
