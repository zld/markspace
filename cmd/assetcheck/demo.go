package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"example.com/assetcheck/internal/fixture"
	"example.com/assetcheck/internal/report"
	"example.com/assetcheck/internal/scan"
)

// runDemo generates every scenario for a seed, checks it and prints the
// per-asset reports plus an overall summary.
func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("demo")
	seed := fs.Int64("seed", 7, "deterministic fixture seed")
	out := fs.String("out", "", "directory for generated fixtures and indexes")
	reuse := fs.Bool("reuse-index", false, "reuse an index still bound to the same input")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "assetcheck demo: %v\n%s\n", err, usage)
		return exitErr
	}
	if *out == "" {
		fmt.Fprintf(stderr, "assetcheck demo: --out is required\n%s\n", usage)
		return exitErr
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(stderr, "assetcheck demo: creating output directory: %v\n", err)
		return exitErr
	}

	assets := fixture.Build(*seed)
	code := exitPass
	anyFail := false
	digests := make(map[string]string, len(assets))
	for _, a := range assets {
		dir := filepath.Join(*out, string(a.Scenario))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(stderr, "assetcheck demo: %v\n", err)
			return exitErr
		}
		mp4Path := filepath.Join(dir, "asset.mp4")
		rtpPath := filepath.Join(dir, "packets.rtp")
		idxPath := filepath.Join(dir, "assetcheck.index.json")
		// With --reuse-index the on-disk inputs are preserved, so a caller
		// can modify one and observe the index being rejected as stale.
		if err := writeIfAbsent(mp4Path, a.MP4, !*reuse); err != nil {
			fmt.Fprintf(stderr, "assetcheck demo: %v\n", err)
			return exitErr
		}
		if err := writeIfAbsent(rtpPath, a.RTP, !*reuse); err != nil {
			fmt.Fprintf(stderr, "assetcheck demo: %v\n", err)
			return exitErr
		}
		// Read back what is on disk rather than the in-memory fixture, so a
		// caller that edits an input between runs sees the index rejected.
		mp4Bytes, err := os.ReadFile(mp4Path)
		if err != nil {
			fmt.Fprintf(stderr, "assetcheck demo: %v\n", err)
			return exitErr
		}
		rtpBytes, err := os.ReadFile(rtpPath)
		if err != nil {
			fmt.Fprintf(stderr, "assetcheck demo: %v\n", err)
			return exitErr
		}
		run, err := process(string(a.Scenario), scan.Input{MP4: mp4Bytes, RTP: rtpBytes}, idxPath, *reuse)
		if err != nil {
			fmt.Fprintf(stderr, "assetcheck demo: %s: %v\n", a.Scenario, err)
			return exitErr
		}
		if err := report.Write(stdout, run); err != nil {
			fmt.Fprintf(stderr, "assetcheck demo: %v\n", err)
			return exitErr
		}
		fmt.Fprintln(stdout)
		digests[string(a.Scenario)] = run.Index.Digest.Value
		if run.Verdict() == report.Fail {
			anyFail = true
		}
		if got := run.Verdict() == report.Pass; !*reuse && got != a.ExpectPass {
			fmt.Fprintf(stderr, "assetcheck demo: %s: expected pass=%v, got %s (%s)\n",
				a.Scenario, a.ExpectPass, verdictText(run), a.Expect)
			code = exitErr
		}
		if a.Scenario == fixture.ScenarioMissing {
			if run.Validate == nil || run.Validate.Missing != 1 {
				fmt.Fprintf(stderr, "assetcheck demo: missing: expected one missing packet\n")
				code = exitErr
			}
		}
		if a.Scenario == fixture.ScenarioDuplicate {
			if run.Validate == nil || run.Validate.Duplicate == 0 || run.Validate.Skew != 0 {
				fmt.Fprintf(stderr, "assetcheck demo: duplicate: expected a counted duplicate with zero skew\n")
				code = exitErr
			}
		}
	}
	printSummary(stdout, *seed, *reuse, digests)

	// A fixture whose outcome contradicts its declared expectation means the
	// harness itself is wrong (exit 2). Otherwise the exit code reflects the
	// assets: 1 if any asset failed validation, 0 if all passed.
	if code != exitPass {
		return code
	}
	if anyFail {
		return exitFail
	}
	return exitPass
}

func verdictText(run *report.Run) string {
	if run.Verdict() == report.Pass {
		return "pass"
	}
	return "fail"
}

func printSummary(w io.Writer, seed int64, reuse bool, digests map[string]string) {
	fmt.Fprintln(w, "demo summary")
	fmt.Fprintf(w, "  seed=%d reuse-index=%t\n", seed, reuse)
	for _, sc := range fixture.AllScenarios {
		fmt.Fprintf(w, "  %-10s index-digest=%s\n", sc, short(digests[string(sc)]))
	}
}

// writeIfAbsent creates path with data unless overwrite is false and the file
// already exists.
func writeIfAbsent(path string, data []byte, overwrite bool) error {
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
	}
	return os.WriteFile(path, data, 0o644)
}
