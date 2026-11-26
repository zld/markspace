package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"continuity/internal/fixture"
	"continuity/internal/manifest"
	"continuity/internal/media"
)

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory for generated samples and recovery state")
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	minCoverage := fs.Int("min-coverage-ppm", 999000, "minimum acceptable coverage in parts per million")
	maxSkewMS := fs.Int("max-av-skew-ms", 20, "maximum audio/video skew in milliseconds")
	maxBitrate := fs.Int("max-bitrate-bps", 2_400_000, "maximum target bitrate")
	budgetMS := fs.Int64("budget-ms", 2000, "maximum resume/processing budget in milliseconds")
	caze := fs.String("case", "all", "all|recovery|corrupt|infeasible|conflict")
	if err := fs.Parse(args); err != nil {
		return exitGeneric
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "demo requires --dir")
		return exitGeneric
	}

	constraints := manifest.Constraints{
		MinCoveragePPM: *minCoverage,
		MaxAVSkewMS:    *maxSkewMS,
		MaxBitrateBPS:  *maxBitrate,
		BudgetMS:       *budgetMS,
	}
	g, err := fixture.Generate(fixture.Options{Dir: *dir, Seed: *seed, Constraints: constraints})
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate fixtures failed: %v\n", err)
		return exitGeneric
	}

	switch *caze {
	case "corrupt":
		return demoCorruptionOnly(g)
	case "infeasible":
		if err := runInfeasibleScenario(g, constraints); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitGeneric
		}
		return exitConstraint
	case "conflict":
		job, pkg, err := preparePlan(g, constraints)
		if err != nil {
			return demoPlanError(err)
		}
		if _, err := runConflictScenario(g, job, pkg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitGeneric
		}
		return exitCheckpointConflict
	case "recovery":
		job, pkg, err := preparePlan(g, constraints)
		if err != nil {
			return demoPlanError(err)
		}
		summary, err := runRecoveryScenario(g, job, pkg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "recovery failed:", err)
			return exitGeneric
		}
		writeJSONSummary(summary)
		return exitOK
	case "all":
		return runDemoAll(g, constraints)
	default:
		fmt.Fprintf(os.Stderr, "unknown --case %s\n", *caze)
		return exitGeneric
	}
}

func runDemoAll(g *fixture.Generated, c manifest.Constraints) int {
	fmt.Printf("fixtures_generated dir=%s seed=%d files=%d defects=%d\n", g.Dir, g.Seed, len(g.Files), len(g.Defects))
	if _, err := checkMigration(g); err != nil {
		fmt.Fprintln(os.Stderr, "migration failed:", err)
		return exitGeneric
	}

	job, pkg, err := preparePlan(g, c)
	if err != nil {
		return demoPlanError(err)
	}
	fmt.Printf("timeline events=%d coverage_ppm=%d max_av_skew_ms=%.3f clusters=%d\n",
		job.index.Report.EventCount, pkg.CoveragePPM, pkg.MaxAVSkewMS, len(job.clusters))
	fmt.Printf("config target_bitrate_bps=%d profile=%s estimated_ms=%d audio_mappings=%d color_decisions=%d reasons=%d\n",
		pkg.Bitrate.TargetBPS, pkg.Bitrate.SelectedProfile, pkg.Budget.EstimatedMS,
		len(pkg.Audio.Mappings), len(pkg.Color.Decisions), len(pkg.Reasons))
	if pkg.CoveragePPM < c.MinCoveragePPM || int64(pkg.MaxAVSkewMS) > int64(c.MaxAVSkewMS) {
		fmt.Fprintln(os.Stderr, "generated normal sample violates acceptance constraints")
		return exitConstraint
	}

	if err := checkRTPWrap(g); err != nil {
		fmt.Fprintln(os.Stderr, "RTP wrap check failed:", err)
		return exitGeneric
	}
	fmt.Printf("emsg_hints=%d first_presentation_ns=%d\n", len(job.hints), job.hints[0].PresentationNS)

	allCorruptOK := true
	var reports []corruptReport
	for _, d := range g.Defects {
		r := verifyDefectFile(g.Files[d.File], d.File, d.Kind, d.Offset)
		reports = append(reports, r)
		printCorruptReport(r)
		if !r.OK {
			allCorruptOK = false
		}
	}
	if !allCorruptOK {
		fmt.Fprintln(os.Stderr, "one or more embedded corruption offsets were not reproduced")
		return exitInputCorrupt
	}

	recovery, err := runRecoveryScenario(g, job, pkg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "recovery scenario failed:", err)
		return exitGeneric
	}
	if _, err := runConflictScenario(g, job, pkg); err != nil {
		fmt.Fprintln(os.Stderr, "conflict scenario failed:", err)
		return exitGeneric
	}
	if err := runInfeasibleScenario(g, c); err != nil {
		fmt.Fprintln(os.Stderr, "infeasible scenario failed:", err)
		return exitGeneric
	}

	summary := map[string]any{
		"status":               "ok",
		"coverage_ppm":         pkg.CoveragePPM,
		"max_av_skew_ms":       pkg.MaxAVSkewMS,
		"target_bitrate_bps":   pkg.Bitrate.TargetBPS,
		"max_bitrate_bps":      c.MaxBitrateBPS,
		"budget_ms":            c.BudgetMS,
		"corrupt_reports":      reports,
		"conflict_exit_code":   exitCheckpointConflict,
		"infeasible_exit_code": exitConstraint,
	}
	for k, v := range recovery {
		summary[k] = v
	}
	writeJSONSummary(summary)
	return exitOK
}

func demoCorruptionOnly(g *fixture.Generated) int {
	ok := true
	for _, d := range g.Defects {
		r := verifyDefectFile(g.Files[d.File], d.File, d.Kind, d.Offset)
		printCorruptReport(r)
		if !r.OK {
			ok = false
		}
	}
	if !ok {
		return exitGeneric
	}
	return exitInputCorrupt
}

func demoPlanError(err error) int {
	fmt.Fprintln(os.Stderr, "plan failed:", err)
	if media.IsConstraintFailure(err) {
		return exitConstraint
	}
	if media.IsInputCorruption(err) {
		return exitInputCorrupt
	}
	return exitGeneric
}

func writeJSONSummary(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "summary encode failed:", err)
		return
	}
	fmt.Println(string(data))
}
