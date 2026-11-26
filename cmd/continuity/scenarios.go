package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"continuity/internal/fixture"
	"continuity/internal/manifest"
	"continuity/internal/media"
	"continuity/internal/resume"
)

func preparePlan(g *fixture.Generated, c manifest.Constraints) (*builtJob, *manifest.ConfigPackage, error) {
	m := fixture.CanonicalManifest(g.Seed, c)
	job, err := buildNormalJob(g, m)
	if err != nil {
		return job, nil, err
	}
	pkg, err := manifest.Plan(manifest.PlanRequest{
		Manifest:     m,
		Report:       job.index.Report,
		ClusterCount: len(job.clusters),
		Inputs:       job.inputs,
		InputPrefix:  job.prefixSHA,
	})
	return job, pkg, err
}

func checkMigration(g *fixture.Generated) (*manifest.MigrationReport, error) {
	rollbackDir := filepath.Join(g.Dir, "migration-rollback")
	m, report, err := manifest.AdaptFile(g.V1Manifest, manifest.AdaptOptions{RollbackDir: rollbackDir})
	if err != nil {
		return report, err
	}
	fmt.Printf("migration original=%s final=%s stages=%v rollback_points=%d tracks=%d\n",
		report.OriginalVersion, report.FinalVersion, report.Stages, len(report.RollbackPoints), len(m.Tracks))
	return report, nil
}

func checkRTPWrap(g *fixture.Generated) error {
	stream, events, err := readLengthPrefixedRTP(g.RTPWrap, 1)
	if err != nil {
		return err
	}
	if stream.LostPackets != 0 {
		return fmt.Errorf("wrap sample unexpectedly lost %d packets", stream.LostPackets)
	}
	seqs := make([]int, 0, len(events))
	for _, e := range events {
		if !e.Lost {
			seqs = append(seqs, e.Sequence)
		}
	}
	fmt.Printf("rtp_wrap packets=%d extended_sequences=%v lost=%d\n", stream.Packets, seqs, stream.LostPackets)
	if len(seqs) != 5 || seqs[0] != 65534 || seqs[4] != 65538 {
		return fmt.Errorf("bad extended RTP sequences: %v", seqs)
	}
	return nil
}

func runRecoveryScenario(g *fixture.Generated, job *builtJob, pkg *manifest.ConfigPackage) (map[string]any, error) {
	interruptDir := filepath.Join(g.Dir, "resume-interrupted")
	base := &resume.Pipeline{
		Dir: interruptDir, JobID: "pwf-demo", Manifest: fixture.CanonicalManifest(g.Seed, pkg.Constraints),
		Config: pkg, Timeline: job.index, Clusters: job.clusters, InterruptAfter: -1,
	}
	interrupt := *base
	interrupt.InterruptAfter = 0
	partial, err := interrupt.Run()
	if !errors.Is(err, resume.ErrSimulatedInterrupt) {
		if err == nil {
			return nil, fmt.Errorf("expected simulated interrupt, pipeline completed")
		}
		return nil, err
	}
	fmt.Printf("interrupted_checkpoint dir=%s committed=%v config_sha=%s\n", interruptDir, partial.Processed, partial.ConfigSHA256)

	resumePipeline := *base
	start := time.Now()
	resumed, err := resumePipeline.Run()
	elapsedMS := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		return nil, err
	}

	replayPipeline := *base
	replay, err := replayPipeline.Run()
	if err != nil {
		return nil, err
	}
	replayMatch := replay.OutputSHA256 == resumed.OutputSHA256

	cleanDir := filepath.Join(g.Dir, "resume-clean")
	clean := *base
	clean.Dir = cleanDir
	cleanResult, err := clean.Run()
	if err != nil {
		return nil, err
	}
	cleanMatch := cleanResult.OutputSHA256 == resumed.OutputSHA256

	fmt.Printf("resume clusters_processed=%v clusters_skipped=%v resume_ms=%.3f\n", resumed.Processed, resumed.Skipped, elapsedMS)
	fmt.Printf("output_sha256=%s replay_sha256_match=%t clean_sha256_match=%t\n", resumed.OutputSHA256, replayMatch, cleanMatch)
	if !replayMatch || !cleanMatch {
		return nil, fmt.Errorf("deterministic output mismatch replay=%v clean=%v", replayMatch, cleanMatch)
	}
	if elapsedMS >= float64(pkg.Constraints.BudgetMS) {
		return nil, fmt.Errorf("resume took %.3f ms, budget %d ms", elapsedMS, pkg.Constraints.BudgetMS)
	}
	return map[string]any{
		"resume_ms":           elapsedMS,
		"output_sha256":       resumed.OutputSHA256,
		"replay_sha256_match": replayMatch,
		"clean_sha256_match":  cleanMatch,
		"config_sha256":       resumed.ConfigSHA256,
		"processed_clusters":  resumed.Processed,
		"skipped_clusters":    resumed.Skipped,
	}, nil
}

func runConflictScenario(g *fixture.Generated, job *builtJob, pkg *manifest.ConfigPackage) (bool, error) {
	conflictDir := filepath.Join(g.Dir, "checkpoint-conflict")
	original := resume.Pipeline{
		Dir: conflictDir, JobID: "conflict-demo", Manifest: fixture.CanonicalManifest(g.Seed, pkg.Constraints),
		Config: pkg, Timeline: job.index, Clusters: job.clusters, InterruptAfter: -1,
	}
	oldResult, err := original.Run()
	if err != nil {
		return false, err
	}
	oldSHA, err := resume.SHA256File(oldResult.OutputPath)
	if err != nil {
		return false, err
	}

	changedManifest := fixture.CanonicalManifest(g.Seed, pkg.Constraints)
	changedManifest.DefaultLFEMix = true
	changedPkg, err := manifest.Plan(manifest.PlanRequest{
		Manifest: changedManifest, Report: job.index.Report, ClusterCount: len(job.clusters),
		Inputs: job.inputs, InputPrefix: job.prefixSHA,
	})
	if err != nil {
		return false, err
	}
	changed := original
	changed.Manifest = changedManifest
	changed.Config = changedPkg
	_, conflictErr := changed.Run()
	if conflictErr == nil || !media.IsConflict(conflictErr) {
		return false, fmt.Errorf("expected checkpoint conflict, got %v", conflictErr)
	}
	newSHA, err := resume.SHA256File(oldResult.OutputPath)
	if err != nil {
		return false, err
	}
	preserved := newSHA == oldSHA
	fmt.Printf("checkpoint_conflict_detected=true old_output_preserved=%t exit_code=4\n", preserved)
	if !preserved {
		return false, fmt.Errorf("old output changed after checkpoint conflict")
	}
	return true, nil
}

func runInfeasibleScenario(g *fixture.Generated, base manifest.Constraints) error {
	c := base
	c.BudgetMS = 1
	c.MaxBitrateBPS = 100_000
	m := fixture.CanonicalManifest(g.Seed, c)
	job, err := buildNormalJob(g, m)
	if err != nil {
		return err
	}
	_, err = manifest.Plan(manifest.PlanRequest{Manifest: m, Report: job.index.Report, ClusterCount: len(job.clusters)})
	if err == nil || !media.IsConstraintFailure(err) {
		return fmt.Errorf("expected constraint infeasibility, got %v", err)
	}
	fmt.Printf("infeasible_detected=true exit_code=3 reason=%v\n", err)
	return nil
}
