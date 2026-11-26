// Command pwf is the terminal entry point for the multi-camera ingest
// workflow. The verify subcommand generates its own fixtures and runs the
// full pipeline, the damaged-input path, the infeasible-budget path and an
// interrupted/resumed run, printing machine-checkable key=value lines.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pwf/internal/pipeline"
	"pwf/internal/quality"
)

// Exit codes mandated by the workflow contract.
const (
	exitOK       = 0
	exitUsage    = 2
	exitMaterial = 3
	exitTask     = 4
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "verify":
		os.Exit(runVerify(os.Args[2:]))
	case "inspect":
		os.Exit(runInspect(os.Args[2:]))
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(exitUsage)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  pwf verify  --work <dir> --seed <int>")
	fmt.Fprintln(os.Stderr, "  pwf inspect --work <dir> --rtp <path> --codec opus|aac [--clock <hz>]")
	fmt.Fprintln(os.Stderr, "  pwf inspect --work <dir> --mp4 <path>")
	os.Exit(exitUsage)
}

func runVerify(args []string) int {
	work, seed, err := parseVerifyArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "argument error:", err)
		return exitUsage
	}
	if err := validateWorkDir(work); err != nil {
		fmt.Fprintln(os.Stderr, "work dir rejected:", err)
		return exitUsage
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "cannot create work dir:", err)
		return exitUsage
	}

	normalDir := filepath.Join(work, "normal")
	damageDir := filepath.Join(work, "damage")
	budgetDir := filepath.Join(work, "budget")
	resumeDir := filepath.Join(work, "resume")
	for _, d := range []string{normalDir, damageDir, budgetDir, resumeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "fixture dir:", err)
			return exitTask
		}
	}
	_ = seed

	// 1. Healthy multi-camera material: two Opus RTP tracks with a gap and
	//    a duplicate, plus one AAC-in-MP4 track.
	sources, err := buildHealthyFixtures(normalDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture generation failed:", err)
		return exitTask
	}
	out, err := pipeline.Run(pipeline.Options{
		WorkDir: normalDir, Sources: sources,
		Thresholds: quality.DefaultThresholds(), Workers: 3,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthy pipeline failed unexpectedly:", err)
		return exitTask
	}
	if out.Manifest == nil || len(out.Manifest.RunSHA256) != 64 {
		fmt.Fprintln(os.Stderr, "full run produced no valid manifest digest")
		return exitTask
	}
	fullSHA := out.Manifest.RunSHA256

	// 2. Damaged RTP input: truncate one frame at a known offset and
	//    confirm the parser reports exactly that byte offset.
	dmgPath, expectedOffset, foundOffset, dmgErr := buildDamagedFixture(damageDir)
	if dmgErr != nil {
		fmt.Fprintln(os.Stderr, "damage fixture failed:", dmgErr)
		return exitTask
	}
	if dmgPath == "" {
		fmt.Fprintln(os.Stderr, "damage fixture path empty")
		return exitTask
	}

	// 3. Infeasible budget: a bitrate-heavy track that demands transcode
	//    under a budget too small to pay for it.
	blocker, budgetErr := runInfeasibleBudget(budgetDir)
	if budgetErr != nil {
		fmt.Fprintln(os.Stderr, "budget scenario failed:", budgetErr)
		return exitTask
	}

	// 4. Interrupted run then resume against the same healthy material.
	resumeSHA, reused, resumeMS, resumeErr := runInterruptAndResume(resumeDir, sources)
	if resumeErr != nil {
		fmt.Fprintln(os.Stderr, "resume scenario failed:", resumeErr)
		return exitTask
	}

	// Machine-checkable report.
	fmt.Printf("damage_expected_offset=%d\n", expectedOffset)
	fmt.Printf("damage_found_offset=%d\n", foundOffset)
	fmt.Printf("offset_error_bytes=%d\n", abs64(foundOffset-expectedOffset))
	fmt.Printf("first_blocker=%s\n", blocker)
	fmt.Printf("resume_ms=%d\n", resumeMS)
	fmt.Printf("reused_nodes=%d\n", reused)
	fmt.Printf("sha256_full=%s\n", fullSHA)
	fmt.Printf("sha256_resume=%s\n", resumeSHA)
	fmt.Printf("sha256_match=%t\n", fullSHA == resumeSHA)

	if foundOffset != expectedOffset {
		fmt.Fprintln(os.Stderr, "VERIFY FAIL: damage offset mismatch")
		return exitTask
	}
	if blocker != "plan" {
		fmt.Fprintln(os.Stderr, "VERIFY FAIL: expected plan blocker, got", blocker)
		return exitTask
	}
	if reused < 2 {
		fmt.Fprintln(os.Stderr, "VERIFY FAIL: expected reused_nodes>=2")
		return exitTask
	}
	if fullSHA != resumeSHA || len(fullSHA) != 64 {
		fmt.Fprintln(os.Stderr, "VERIFY FAIL: full/resume manifest digests differ or malformed")
		return exitTask
	}
	fmt.Println("verify=ok")
	return exitOK
}

func parseVerifyArgs(args []string) (string, int, error) {
	var work string
	seed := 41
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--work":
			i++
			if i >= len(args) {
				return "", 0, fmt.Errorf("--work requires a value")
			}
			work = args[i]
		case "--seed":
			i++
			if i >= len(args) {
				return "", 0, fmt.Errorf("--seed requires a value")
			}
			if _, err := fmt.Sscanf(args[i], "%d", &seed); err != nil {
				return "", 0, fmt.Errorf("--seed must be an integer")
			}
		default:
			return "", 0, fmt.Errorf("unknown flag %q", args[i])
		}
	}
	if work == "" {
		return "", 0, fmt.Errorf("--work is required")
	}
	return work, seed, nil
}

// validateWorkDir rejects any path containing a ".." traversal component,
// whether the path is absolute or relative.
func validateWorkDir(work string) error {
	cleaned := filepath.Clean(work)
	vol := filepath.VolumeName(cleaned)
	rest := cleaned[len(vol):]
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == ".." {
			return fmt.Errorf("path traversal rejected: %s", work)
		}
	}
	return nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
