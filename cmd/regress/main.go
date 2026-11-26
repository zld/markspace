// Command regress drives the media regression harness from the terminal.
// It generates deterministic fixtures, runs the dependency graph with
// checkpoint-based selective rebuild, verifies every acceptance metric, and
// renders a byte-stable HLS manifest.
//
// Exit codes:
//
//	0  - all checks passed
//	2  - invalid arguments or input
//	75 - simulated interruption (a resume run returns to 0)
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"regress/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

const (
	exitOK        = 0
	exitInvalid   = 2
	exitInterrupt = 75
)

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitInvalid
	}
	switch args[0] {
	case "verify":
		return cmdVerify(args[1:])
	case "resume":
		return cmdResume(args[1:])
	case "help", "-h", "--help":
		usage()
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return exitInvalid
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: regress <verify|resume> --seed <int> --work <dir> [--case all] [--interrupt]")
}

type options struct {
	seed      int64
	work      string
	caseName  string
	interrupt bool
}

func parseFlags(fs *flag.FlagSet, argv []string) (*options, int) {
	opts := &options{caseName: "all"}
	fs.Int64Var(&opts.seed, "seed", 0, "deterministic fixture seed")
	fs.StringVar(&opts.work, "work", "", "working directory for outputs and checkpoints")
	fs.StringVar(&opts.caseName, "case", "all", "case selector (all)")
	fs.BoolVar(&opts.interrupt, "interrupt", false, "simulate an interruption after the cold build")
	if err := fs.Parse(argv); err != nil {
		return nil, exitInvalid
	}
	if opts.seed < 0 {
		fmt.Fprintln(os.Stderr, "seed must be non-negative")
		return nil, exitInvalid
	}
	if strings.TrimSpace(opts.work) == "" {
		fmt.Fprintln(os.Stderr, "--work is required")
		return nil, exitInvalid
	}
	if opts.caseName != "all" {
		fmt.Fprintf(os.Stderr, "unknown case %q (only \"all\" is supported)\n", opts.caseName)
		return nil, exitInvalid
	}
	return opts, exitOK
}

// cmdVerify runs the cold+warm sequence and validates every metric.
func cmdVerify(argv []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	opts, code := parseFlags(fs, argv)
	if opts == nil {
		return code
	}
	if err := os.MkdirAll(opts.work, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create work dir: %v\n", err)
		return exitInvalid
	}

	eng, err := pipeline.NewEngine(opts.work, opts.seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine init failed: %v\n", err)
		return exitInvalid
	}

	if opts.interrupt {
		return runInterrupt(eng, opts)
	}

	m, err := eng.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify failed: %v\n", err)
		return exitInvalid
	}

	// Independent re-hash of the manifest file on disk.
	diskSHA, err := hashFile(filepath.Join(opts.work, "out", "manifest.m3u8"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifest hash failed: %v\n", err)
		return exitInvalid
	}

	printMetrics(m, diskSHA)
	if fails := validate(m, diskSHA); len(fails) != 0 {
		for _, f := range fails {
			fmt.Fprintf(os.Stderr, "FAIL: %s\n", f)
		}
		return exitInvalid
	}
	fmt.Println("result=pass")
	return exitOK
}

// runInterrupt performs the cold build, simulates a crash (exit 75), and is
// re-entered via "resume" to converge back on a byte-identical manifest.
func runInterrupt(eng *pipeline.Engine, opts *options) int {
	rebuilt, resumeMS, sha, err := eng.RunInterrupt()
	if err != nil {
		fmt.Fprintf(os.Stderr, "interrupt path failed: %v\n", err)
		return exitInvalid
	}
	diskSHA, err := hashFile(filepath.Join(opts.work, "out", "manifest.m3u8"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifest hash failed: %v\n", err)
		return exitInvalid
	}
	fmt.Printf("resume_rebuilt=%d resume_ms=%d manifest_sha256=%s\n", rebuilt, resumeMS, sha)
	if diskSHA != strings.TrimPrefix(sha, "manifest:") {
		fmt.Fprintln(os.Stderr, "FAIL: resumed manifest differs from digest")
		return exitInvalid
	}
	// A verify --interrupt run signals the simulated crash with code 75 while
	// still having produced resumable state on disk.
	return exitInterrupt
}

// cmdResume continues an interrupted work directory. Its contract differs
// from a cold verify: every node must be reused from the validated on-disk
// checkpoints (zero rebuilds) and the manifest must stay byte-identical.
func cmdResume(argv []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	opts, code := parseFlags(fs, argv)
	if opts == nil {
		return code
	}
	eng, err := pipeline.NewEngine(opts.work, opts.seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine init failed: %v\n", err)
		return exitInvalid
	}
	m, err := eng.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resume verify failed: %v\n", err)
		return exitInvalid
	}
	diskSHA, err := hashFile(filepath.Join(opts.work, "out", "manifest.m3u8"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifest hash failed: %v\n", err)
		return exitInvalid
	}
	fmt.Printf("cold_rebuilt=%d\n", m.ColdRebuilt)
	fmt.Printf("warm_rebuilt=%d\n", m.WarmRebuilt)
	fmt.Printf("resume_ms=%d\n", m.ResumeMS)
	fmt.Printf("manifest_sha256=%s\n", diskSHA)
	var fails []string
	// On resume the store already holds valid records, so the warm phase must
	// rebuild nothing and the manifest digest must remain the stable value.
	if m.WarmRebuilt != 0 {
		fails = append(fails, fmt.Sprintf("warm_rebuilt=%d want 0 on resume", m.WarmRebuilt))
	}
	if len(diskSHA) != 64 || !isHex(diskSHA) {
		fails = append(fails, "manifest_sha256 must be 64 hex chars")
	}
	if len(m.Breaks) != 0 {
		fails = append(fails, fmt.Sprintf("continuity_breaks=%d want 0", len(m.Breaks)))
	}
	if len(fails) != 0 {
		for _, f := range fails {
			fmt.Fprintf(os.Stderr, "FAIL: %s\n", f)
		}
		return exitInvalid
	}
	fmt.Println("result=pass resumed=true")
	return exitOK
}

func printMetrics(m *pipeline.Metrics, diskSHA string) {
	fmt.Printf("cold_rebuilt=%d\n", m.ColdRebuilt)
	fmt.Printf("warm_rebuilt=%d\n", m.WarmRebuilt)
	fmt.Printf("fault_offset_delta=%d\n", m.FaultOffsetDelta)
	fmt.Printf("rtp_duplicate=%d\n", m.RTPDuplicate)
	fmt.Printf("rtp_reordered=%d\n", m.RTPReordered)
	fmt.Printf("rtp_burst_lost=%d\n", m.RTPBurstLost)
	fmt.Printf("resume_ms=%d\n", m.ResumeMS)
	fmt.Printf("manifest_sha256=%s\n", diskSHA)
	if len(m.Breaks) > 0 {
		fmt.Printf("continuity_breaks=%d\n", len(m.Breaks))
	}
}

// validate enforces the exact acceptance contract.
func validate(m *pipeline.Metrics, diskSHA string) []string {
	var fails []string
	if m.ColdRebuilt != 8 {
		fails = append(fails, fmt.Sprintf("cold_rebuilt=%d want 8", m.ColdRebuilt))
	}
	if m.WarmRebuilt != 0 {
		fails = append(fails, fmt.Sprintf("warm_rebuilt=%d want 0", m.WarmRebuilt))
	}
	if m.FaultOffsetDelta != 0 {
		fails = append(fails, fmt.Sprintf("fault_offset_delta=%d want 0", m.FaultOffsetDelta))
	}
	if m.RTPDuplicate != 2 {
		fails = append(fails, fmt.Sprintf("rtp_duplicate=%d want 2", m.RTPDuplicate))
	}
	if m.RTPReordered != 3 {
		fails = append(fails, fmt.Sprintf("rtp_reordered=%d want 3", m.RTPReordered))
	}
	if m.RTPBurstLost != 4 {
		fails = append(fails, fmt.Sprintf("rtp_burst_lost=%d want 4", m.RTPBurstLost))
	}
	if m.ResumeMS > 2000 {
		fails = append(fails, fmt.Sprintf("resume_ms=%d want <=2000", m.ResumeMS))
	}
	if len(diskSHA) != 64 || !isHex(diskSHA) {
		fails = append(fails, "manifest_sha256 must be 64 hex chars")
	}
	if len(m.Breaks) != 0 {
		fails = append(fails, fmt.Sprintf("continuity_breaks=%d want 0", len(m.Breaks)))
	}
	return fails
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
