// Command mediamerge merges remote multi-track captures into synchronized,
// verifiable media packages. Pure terminal interface; all state lives in a
// local work directory.
//
// Exit codes: 0 ok, 2 invalid input/arguments, 3 media validation failed,
// 4 recovery/commit failed, 5 cross-stage invariant failed.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediamerge/internal/container"
	"mediamerge/internal/txn"
)

const (
	exitOK        = 0
	exitUsage     = 2
	exitMedia     = 3
	exitCommit    = 4
	exitInvariant = 5
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mediamerge <demo|verify> [flags]")
		return exitUsage
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", args[0])
		return exitUsage
	}
}

func parseConfig(name string, args []string) (config, string, error) {
	var cfg config
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Int64Var(&cfg.seed, "seed", 17, "deterministic fixture seed")
	fs.Int64Var(&cfg.targetBps, "target-bitrate", 4000000, "target bitrate in bps")
	fs.Uint64Var(&cfg.jitterWindow, "jitter-window", 64, "jitter buffer window in packets")
	fs.StringVar(&cfg.faultSet, "fault-set", "all", "fault set: all|container|rtp|sync|manifest|none")
	fs.BoolVar(&cfg.resume, "resume", false, "simulate crash and resume from WAL")
	fs.IntVar(&cfg.repeat, "repeat", 1, "verify repetitions")
	fs.BoolVar(&cfg.checkManifest, "check-manifest", false, "validate manifest invariants")
	workDir := fs.String("work-dir", "", "work directory (default: .mediamerge-<subcommand>)")
	if err := fs.Parse(args); err != nil {
		return cfg, "", err
	}
	if cfg.seed < 0 || cfg.targetBps < 1000 || cfg.targetBps > 1<<30 {
		return cfg, "", fmt.Errorf("invalid seed or target-bitrate")
	}
	if cfg.jitterWindow == 0 || cfg.jitterWindow > 4096 {
		return cfg, "", fmt.Errorf("jitter-window out of range (0,4096]")
	}
	switch cfg.faultSet {
	case "all", "container", "rtp", "sync", "manifest", "none":
	default:
		return cfg, "", fmt.Errorf("unknown fault-set %q", cfg.faultSet)
	}
	if cfg.repeat < 1 || cfg.repeat > 16 {
		return cfg, "", fmt.Errorf("repeat out of range [1,16]")
	}
	wd := *workDir
	if wd == "" {
		wd = filepath.Join(".", ".mediamerge-"+name)
	}
	clean, err := filepath.Abs(wd)
	if err != nil {
		return cfg, "", err
	}
	if strings.Contains(clean, "..") {
		return cfg, "", fmt.Errorf("work-dir escapes: %s", wd)
	}
	return cfg, clean, nil
}

func runDemo(args []string) int {
	cfg, workDir, err := parseConfig("demo", args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	if cfg.resume {
		// First pass: crash after the transport stage; second pass resumes.
		if _, err := runPipeline(cfg, workDir, "transport"); err != nil && !errors.Is(err, errCrash) {
			return fail(err)
		} else if err == nil {
			fmt.Fprintln(os.Stderr, "error: crash simulation did not trigger")
			return exitUsage
		}
		fmt.Println("stage=crash-simulated after=transport")
	}
	rep, err := runPipeline(cfg, workDir, "")
	if err != nil {
		return fail(err)
	}
	printReport(rep)
	return checkAcceptance(rep)
}

func runVerify(args []string) int {
	cfg, workDir, err := parseConfig("verify", args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	var firstSHA string
	for i := 0; i < cfg.repeat; i++ {
		dir := fmt.Sprintf("%s-run%d", workDir, i)
		rep, err := runPipeline(cfg, dir, "")
		if err != nil {
			return fail(err)
		}
		fmt.Printf("verify_run=%d provenance_sha256=%s\n", i, rep.provSHA)
		if i == 0 {
			firstSHA = rep.provSHA
		} else if rep.provSHA != firstSHA {
			fmt.Fprintln(os.Stderr, "invariant: provenance differs across identical runs")
			return exitInvariant
		}
		if code := checkAcceptance(rep); code != exitOK {
			return code
		}
		if cfg.checkManifest {
			if err := rep.m.Validate(); err != nil {
				fmt.Fprintln(os.Stderr, "invariant:", err)
				return exitInvariant
			}
			fmt.Printf("manifest_check=ok run=%d\n", i)
		}
	}
	fmt.Printf("provenance_sha256=%s\nverify=ok repeat=%d\n", firstSHA, cfg.repeat)
	return exitOK
}

// checkAcceptance enforces the media-validation bounds on the normal path.
func checkAcceptance(rep *report) int {
	m := rep.m
	if m.BitrateErrorPct() > 5.0 {
		fmt.Fprintf(os.Stderr, "media: bitrate error %.2f%% exceeds 5%%\n", m.BitrateErrorPct())
		return exitMedia
	}
	if m.AVSkewMs() > 20.0 {
		fmt.Fprintf(os.Stderr, "media: A/V skew %.3f ms exceeds 20 ms\n", m.AVSkewMs())
		return exitMedia
	}
	if m.AlignmentTicks() > 1 {
		fmt.Fprintf(os.Stderr, "invariant: manifest alignment %d ticks exceeds 1\n", m.AlignmentTicks())
		return exitInvariant
	}
	return exitOK
}

func printReport(rep *report) {
	for _, s := range rep.stages {
		fmt.Printf("stage=%s status=committed\n", s)
	}
	for _, f := range rep.faults {
		fmt.Printf("fault stage=%s kind=%s count=%d\n", f.Stage, f.Kind, f.Count)
	}
	m := rep.m
	fmt.Printf("target_bps=%d\n", m.TargetBps)
	fmt.Printf("actual_bps=%d\n", m.ActualBps)
	fmt.Printf("bitrate_error_pct=%.3f\n", m.BitrateErrorPct())
	fmt.Printf("packet_loss=%d\n", m.Quality.PacketLoss)
	fmt.Printf("recovered_packets=%d\n", m.Quality.RecoveredPacket)
	fmt.Printf("av_skew_ms=%.3f\n", m.AVSkewMs())
	fmt.Printf("manifest_alignment_ticks=%d\n", m.AlignmentTicks())
	var rs []string
	for _, r := range m.CommittedRanges {
		rs = append(rs, fmt.Sprintf("[%d,%d)", r.Start, r.End))
	}
	fmt.Printf("committed_ranges=%s\n", strings.Join(rs, ","))
	fmt.Printf("rollback_count=%d\n", rep.rollbacks)
	fmt.Printf("provenance_sha256=%s\n", rep.provSHA)
}

// fail maps an error to its exit code.
func fail(err error) int {
	msg := err.Error()
	fmt.Fprintln(os.Stderr, "error:", msg)
	var be *container.BoxError
	switch {
	case errors.As(err, &be), strings.HasPrefix(msg, "media:"):
		return exitMedia
	case strings.HasPrefix(msg, "commit:"), errors.Is(err, txn.ErrInputChanged):
		return exitCommit
	case strings.HasPrefix(msg, "invariant:"):
		return exitInvariant
	default:
		return exitMedia
	}
}
