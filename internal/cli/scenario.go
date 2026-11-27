// Package cli: the scripted damaged-release scenario.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"example.com/assetpack/internal/audio"
	"example.com/assetpack/internal/fixture"
	"example.com/assetpack/internal/pipeline"
)

// runFaultDemo scripts the three-act damaged release: a healthy baseline
// release, a damaged attempt that is rolled back, and a resumed re-run that
// reaches committed.  Every act starts from the healthy fixture so the
// baseline digest is stable across invocations.
func runFaultDemo(opts pipeline.Options, d demoFlags, set fixture.FaultSet, target audio.Target, stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "fault injection: %s", strings.Join(set.Names(), ","))
	if d.failAfter != "" {
		fmt.Fprintf(stdout, "  fail-after=%s", d.failAfter)
	}
	fmt.Fprintln(stdout)

	// Reset the work tree: act 2 must be exactly the first damaged attempt,
	// not a second attempt against an already-rolled-back directory.
	if err := os.RemoveAll(opts.ReleaseDir); err != nil {
		return usageError(stderr, err)
	}
	if err := os.RemoveAll(opts.StateDir); err != nil {
		return usageError(stderr, err)
	}

	// Act 1: healthy baseline, with no stage failure injected.
	fmt.Fprintln(stdout, "== act 1: healthy baseline release")
	if err := prepare(d.work, fixture.FaultSet{}, target, d.release); err != nil {
		return usageError(stderr, err)
	}
	baseOpts := opts
	baseOpts.FailAfter = ""
	base, err := pipeline.Run(baseOpts)
	if err != nil {
		render(base, err, false, stdout, stderr)
		fmt.Fprintln(stderr, "baseline release failed; cannot run the damaged scenario")
		return 1
	}
	if !base.Valid {
		fmt.Fprintln(stderr, "baseline release is not valid; cannot run the damaged scenario")
		return 1
	}
	goodDigest := base.ManifestSHA
	fmt.Fprintf(stdout, "baseline manifest digest: %s (%d resources)\n", goodDigest, len(base.Assets))

	// Act 2: damaged inputs with the requested stage failure.
	fmt.Fprintln(stdout, "== act 2: damaged inputs")
	if err := prepare(d.work, set, target, d.release); err != nil {
		return usageError(stderr, err)
	}
	failed, failErr := pipeline.Run(opts)
	render(failed, failErr, true, stdout, stderr)
	if failed == nil {
		return 1
	}
	unchanged := failed.ManifestSHA == goodDigest && failed.After != nil && failed.Before != nil &&
		failed.After.Digest == failed.Before.Digest
	if d.failAfter != "" && !unchanged {
		fmt.Fprintf(stderr, "rollback did not restore the previous manifest: got %s want %s\n",
			failed.ManifestSHA, goodDigest)
		return 1
	}
	if d.failAfter != "" {
		fmt.Fprintf(stdout, "rollback verified: manifest still %s\n", goodDigest)
	}

	// Act 3: clean inputs, resumed run with no failure injected.
	fmt.Fprintln(stdout, "== act 3: clean inputs, resumed run")
	if err := prepare(d.work, fixture.FaultSet{}, target, d.release); err != nil {
		return usageError(stderr, err)
	}
	resumeOpts := opts
	resumeOpts.FailAfter = ""
	resumed, resumeErr := pipeline.Run(resumeOpts)
	render(resumed, resumeErr, true, stdout, stderr)
	if resumed == nil {
		return 1
	}
	return resumed.ExitCode()
}
