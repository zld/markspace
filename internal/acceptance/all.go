package acceptance

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func init() {
	RegisterScenario("all", runAll)
}

func runAll(opts Options, out io.Writer) int {
	if err := os.MkdirAll(opts.Root, 0o755); err != nil {
		fmt.Fprintf(out, "write_error=mkdir root: %v\n", err)
		return ExitInputOrWrite
	}
	passed, failed := 0, 0
	check := func(name string, ok bool) {
		if ok {
			passed++
			fmt.Fprintf(out, "coverage=%s result=pass\n", name)
		} else {
			failed++
			fmt.Fprintf(out, "coverage=%s result=fail\n", name)
		}
	}

	normalCode, _ := runNormalInRoot(opts.Root, opts.CheckpointFrames, true, out)
	check("normal_acceptance_values", normalCode == ExitOK)

	fxRoot := filepath.Join(opts.Root, "fixtures")
	check("bad_checksum", checksumCoverage(filepath.Join(fxRoot, "bad_checksum"), out))
	check("truncated_mp4", truncatedMP4Coverage(filepath.Join(fxRoot, "truncated_mp4"), out))
	check("rtp_half_period", rtpAmbiguousCoverage(out))
	check("rtp_unique_wrap", rtpWrapCoverage(out))
	check("same_path_replace", replaceCoverage(filepath.Join(fxRoot, "same_path_replace"), out))
	check("metadata_only", metadataCoverage(filepath.Join(fxRoot, "metadata_only"), out))
	check("packaging_cache", packagingCoverage(filepath.Join(fxRoot, "packaging_cache"), out))
	check("rollback", rollbackCoverage(filepath.Join(fxRoot, "rollback"), out))

	recoveryRoot := filepath.Join(opts.Root, "recovery")
	if interruptCoverage(recoveryRoot, 1, out) {
		check("chunk_interrupt", true)
		check("resume_after_interrupt", resumeCoverage(recoveryRoot, out))
	} else {
		check("chunk_interrupt", false)
	}

	if failed > 0 {
		fmt.Fprintf(out, "scenario=all status=failed checks_passed=%d checks_failed=%d\n", passed, failed)
		return ExitUsage
	}
	fmt.Fprintf(out, "scenario=all status=ok checks_passed=%d checks_failed=0\n", passed)
	return ExitOK
}

func runRollbackScenario(opts Options, out io.Writer) int {
	if rollbackCoverage(opts.Root, out) {
		return ExitOK
	}
	return ExitUsage
}
