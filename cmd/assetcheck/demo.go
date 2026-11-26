package main

import (
	"fmt"
	"io"
	"path/filepath"

	"assetcheck/internal/fixture"
	"assetcheck/internal/media"
	"assetcheck/internal/task"
)

func runDemo(out io.Writer, dir, caseName string, redeliver int) error {
	if redeliver <= 0 {
		redeliver = 2
	}
	switch caseName {
	case "clean":
		return demoClean(out, dir, redeliver)
	case "replace-identical":
		return demoReplaceIdentical(out, dir, redeliver)
	case "metadata-change":
		return demoMetadataChange(out, dir, redeliver)
	default:
		return media.SpecMismatch("DEMO_UNKNOWN_CASE", "unknown demo case").WithDetail(caseName)
	}
}

func printCoreMetrics(out io.Writer, r *task.Result) {
	if r == nil || r.Report == nil {
		return
	}
	rep := r.Report
	fmt.Fprintf(out, "target_bps=%d\n", rep.TargetBPS)
	fmt.Fprintf(out, "actual_bps=%d\n", rep.ActualBPS)
	fmt.Fprintf(out, "delta_pct=%.2f\n", rep.DeltaPct)
	fmt.Fprintf(out, "psnr_db=%.2f\n", rep.PSNRDB)
	fmt.Fprintf(out, "align_max_ms=%.2f\n", rep.AlignMaxMS)
}

func printManifest(out io.Writer, r *task.Result) {
	if r != nil {
		fmt.Fprintf(out, "manifest_sha256=%s\n", r.ManifestSHA256)
	}
}

func verifyResults(results []*task.Result) error {
	if !sameManifestSHA(results) {
		return media.SpecMismatch("DEMO_NONDETERMINISTIC_MANIFEST", "duplicate deliveries produced different manifests")
	}
	return nil
}

func demoClean(out io.Writer, dir string, redeliver int) error {
	opts := fixture.DefaultOptions()
	source := filepath.Join(dir, "clip.mkv")
	work := filepath.Join(dir, "work")
	if err := fixture.WriteFile(source, opts); err != nil {
		return err
	}
	results, err := deliverConcurrent(work, demoInput(source, opts.AssetID, ""), redeliver)
	if err != nil {
		return err
	}
	if err := verifyResults(results); err != nil {
		return err
	}
	primary := committedResult(results)
	if countCommitted(results) != 1 {
		return media.SpecMismatch("DEMO_DUPLICATE_COMMIT", "concurrent deliveries did not commit exactly once").
			WithDetail(fmt.Sprintf("committed=%d deliveries=%d", countCommitted(results), len(results)))
	}
	printCoreMetrics(out, primary)
	fmt.Fprintf(out, "deliveries=%d committed=%d deduplicated=%d\n",
		len(results), countCommitted(results), len(results)-countCommitted(results))
	printManifest(out, primary)
	return nil
}
