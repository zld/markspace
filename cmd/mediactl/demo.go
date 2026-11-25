package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediaflow/internal/audit"
	"mediaflow/internal/batch"
	"mediaflow/internal/fixture"
	"mediaflow/internal/index"
	"mediaflow/internal/probe"
	"mediaflow/internal/safepath"
)

func generateFixtures(work string, c *commonFlags) (*probe.Manifest, string, error) {
	return fixture.GenerateDemo(work, fixture.Options{
		BatchKey:     "acceptance",
		BudgetMbps:   c.budgetMbps,
		QualityMin:   c.qualityMin,
		ChunkSamples: c.chunkSamples,
	})
}

func loadManifest(path string) (*probe.Manifest, error) {
	return probe.LoadManifest(path)
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := bindCommon(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if code := c.validate(); code != 0 {
		fmt.Fprintln(stderr, "invalid demo parameters")
		return code
	}
	work, err := prepareWork(c.work)
	if err != nil {
		fmt.Fprintf(stderr, "prepare work: %v\n", err)
		return 2
	}
	m, manifestPath, err := generateFixtures(work, c)
	if err != nil {
		fmt.Fprintf(stderr, "generate fixtures: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "generated fixtures and manifest=%s\n", manifestPath)
	if err := resetDemoState(work, m); err != nil {
		fmt.Fprintf(stderr, "reset demo checkpoint state: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "== phase 1: normal offline batch ==")
	normal := batch.RunManifest(m, batch.Options{WorkDir: work, BatchKey: "demo-normal"})
	normal.PrintText(stdout)
	writeSummaryJSON(work, "normal-summary.json", normal)
	normalStable, err := audit.StableDigest(normal)
	if err != nil {
		fmt.Fprintf(stderr, "stable digest: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "== phase 2: fault-injected batch (chunk 1 interrupted) ==")
	faultSummary := batch.RunManifest(m, batch.Options{
		WorkDir: work, BatchKey: "demo-recovery",
		Hooks: batch.Hooks{AfterFirstSample: injectedFault},
	})
	faultSummary.PrintText(stdout)
	writeSummaryJSON(work, "fault-summary.json", faultSummary)

	fmt.Fprintln(stdout, "== phase 3: recovery rerun ==")
	recovered := batch.RunManifest(m, batch.Options{WorkDir: work, BatchKey: "demo-recovery"})
	recovered.PrintText(stdout)
	writeSummaryJSON(work, "recovery-summary.json", recovered)

	fmt.Fprintln(stdout, "== phase 4: identical-parameter repeat ==")
	repeated := batch.RunManifest(m, batch.Options{WorkDir: work, BatchKey: "demo-normal"})
	repeated.PrintText(stdout)
	writeSummaryJSON(work, "repeat-summary.json", repeated)
	repeatStable, _ := audit.StableDigest(repeated)

	failures := verifyDemoAcceptance(normal, faultSummary, recovered)
	if normalStable != repeatStable {
		failures = append(failures, fmt.Sprintf("stable summary changed on repeat: %s != %s", normalStable, repeatStable))
	}
	fmt.Fprintf(stdout, "stable_summary_digest=%s\n", normalStable)
	if len(failures) > 0 {
		fmt.Fprintln(stdout, "DEMO ACCEPTANCE: FAIL")
		for _, msg := range failures {
			fmt.Fprintln(stdout, "  - "+msg)
		}
		return 1
	}
	fmt.Fprintln(stdout, "DEMO ACCEPTANCE: PASS")
	fmt.Fprintln(stdout, "underlying batch exit codes: normal=1 expected_item_failures, fault=1 injected_chunk_failure, recovery=1 expected_item_failures; `mediactl run` returns the batch exit code directly")
	return 0
}

func injectedFault(itemID string, chunkIndex int, _ string) error {
	if itemID == "good-1080p30" && chunkIndex == 1 {
		return errors.New("injected interruption after partial chunk write")
	}
	return nil
}

// resetDemoState removes only the two fixed demo batch identities. This lets the
// fault scenario be replayed when the same demo command is run repeatedly.
func resetDemoState(work string, m *probe.Manifest) error {
	for _, batchKey := range []string{"demo-normal", "demo-recovery"} {
		for _, item := range m.Items {
			cpPath, err := index.PathFor(work, batchKey, item.ID)
			if err != nil {
				return err
			}
			stem := filepath.Base(cpPath)
			stem = stem[:len(stem)-len(".json")]
			chunkDir, err := safepath.Resolve(work, "chunks", stem)
			if err != nil {
				return err
			}
			outputPath, err := safepath.Resolve(work, "outputs", stem+".mp4")
			if err != nil {
				return err
			}
			_ = os.Remove(cpPath)
			_ = os.RemoveAll(chunkDir)
			_ = os.Remove(outputPath)
		}
	}
	return nil
}

func writeSummaryJSON(work, name string, s *audit.Summary) {
	path, err := safepath.Resolve(work, name)
	if err != nil {
		return
	}
	_ = s.WriteJSON(path)
}

func findItem(s *audit.Summary, id string) *audit.ItemReport {
	for _, it := range s.Items {
		if it.ItemID == id {
			return it
		}
	}
	return nil
}

func verifyDemoAcceptance(normal, faultRun, recovered *audit.Summary) []string {
	var failures []string
	good := findItem(normal, "good-1080p30")
	if good == nil || good.Status != audit.StatusPass {
		failures = append(failures, "good asset did not pass normal batch")
	} else if !good.BitrateWithinTol {
		failures = append(failures, fmt.Sprintf("good bitrate variance outside tolerance: target=%d actual=%d", good.TargetBitrateKbps, good.ActualBitrateKbps))
	}

	faultGood := findItem(faultRun, "good-1080p30")
	if faultGood == nil || faultGood.Status != audit.StatusChunkFailed {
		failures = append(failures, "fault injection did not produce chunk_failed for good asset")
	}

	recGood := findItem(recovered, "good-1080p30")
	if recGood == nil || recGood.Status != audit.StatusPass {
		failures = append(failures, "good asset did not pass after recovery")
	} else {
		if !containsInt(recGood.RolledBackChunks, 1) {
			failures = append(failures, fmt.Sprintf("tail chunk was not rolled back: %v", recGood.RolledBackChunks))
		}
		if !containsInt(recGood.SkippedChunks, 0) {
			failures = append(failures, fmt.Sprintf("verified chunk 0 was not skipped: %v", recGood.SkippedChunks))
		}
		if good != nil && recGood.OutputDigest != good.OutputDigest {
			failures = append(failures, "recovered output digest differs from uninterrupted output")
		}
	}

	big := findItem(normal, "policy-8k60")
	if big == nil || big.Status != audit.StatusBudgetFailed {
		failures = append(failures, "impossible budget was not explicitly rejected")
	} else if !hasDeviation(big, "bitrate_budget") {
		failures = append(failures, "budget failure lacks bitrate_budget deviation")
	}

	sparse := findItem(normal, "boundary-sparse-keyframes")
	if sparse == nil || sparse.Status != audit.StatusQualityFailed || !hasClass(sparse, "sparse_keyframes") {
		failures = append(failures, "sparse keyframe boundary did not propagate to quality failure")
	}
	nonzero := findItem(normal, "boundary-nonzero-start")
	if nonzero == nil || !hasClass(nonzero, "non_zero_start") {
		failures = append(failures, "nonzero-start interval was not reported")
	}
	edit := findItem(normal, "corrupt-edit-oob")
	if edit == nil || edit.Status != audit.StatusRejected || !hasClass(edit, "edit_list_out_of_bounds") {
		failures = append(failures, "OOB edit did not reject with mapped interval")
	}
	lengthBad := findItem(normal, "corrupt-box-length")
	if lengthBad == nil || lengthBad.Status != audit.StatusRejected || !hasClass(lengthBad, "box_length_overflow") {
		failures = append(failures, "bad box length did not reject with byte interval")
	}
	truncBad := findItem(normal, "corrupt-truncated-box")
	if truncBad == nil || truncBad.Status != audit.StatusRejected || !hasClass(truncBad, "truncated_box") {
		failures = append(failures, "truncated box did not reject with byte interval")
	}
	missing := findItem(normal, "invalid-missing-audio")
	if missing == nil || missing.Status != audit.StatusRejected || !hasClass(missing, "missing_track") {
		failures = append(failures, "missing track expectation was not reported")
	}
	return failures
}

func containsInt(xs []int, want int) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func hasClass(r *audit.ItemReport, class string) bool {
	if r == nil {
		return false
	}
	for _, in := range r.Intervals {
		if string(in.Class) == class {
			return true
		}
	}
	return false
}

func hasDeviation(r *audit.ItemReport, constraint string) bool {
	if r == nil || r.Config == nil {
		return false
	}
	for _, d := range r.Config.Decision.Deviations {
		if d.Constraint == constraint {
			return true
		}
	}
	return false
}
