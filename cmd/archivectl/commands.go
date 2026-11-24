package main

import (
	"fmt"
	"os"

	"archive-recovery/internal/adapter"
	"archive-recovery/internal/index"
	"archive-recovery/internal/job"
	"archive-recovery/internal/media"
	"archive-recovery/internal/testkit"
)

// runDemo generates a corpus, executes the recovery job (possibly with a
// controlled interruption) and prints the acceptance key/value fields.
func runDemo(o cliOptions) int {
	corpus, err := testkit.Generate(o.codec, o.scenario, o.seed, o.chunkBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivectl: %v\n", err)
		return media.ClassOf(err).ExitCode()
	}
	in := job.JobInput{
		Codec: o.codec, ChunkBytes: o.chunkBytes, Seed: o.seed,
		Stream: corpus.Stream, PTS: corpus.PTS, LegacyIndex: corpus.LegacyIndex,
		InterruptAfter: o.interruptAfter, Resume: o.resume, WorkDir: o.workdir,
	}
	result, err := job.Run(in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivectl: %v\n", err)
		return media.ClassOf(err).ExitCode()
	}
	printResult(o, result)
	if result.Status == job.StatusInterrupted {
		// An intentional interruption is not a failure; resume continues it.
		return 0
	}
	if result.RecoverableError {
		return media.ClassRecoverable.ExitCode()
	}
	if !verifyMetrics(o, result) {
		return media.ClassCheckpoint.ExitCode()
	}
	return 0
}

// runVerify re-runs the normal corpus independently and checks that its
// manifest digest matches the digest recorded in the demo workdir, proving
// repeatable recovery from identical input bytes.
func runVerify(o cliOptions) int {
	corpus, err := testkit.Generate(o.codec, testkit.ScenarioNormal, o.seed, o.chunkBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivectl: %v\n", err)
		return media.ClassOf(err).ExitCode()
	}
	verifyDir := o.workdir + "-verify"
	result, err := job.Run(job.JobInput{
		Codec: o.codec, ChunkBytes: o.chunkBytes, Seed: o.seed,
		Stream: corpus.Stream, PTS: corpus.PTS, WorkDir: verifyDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivectl: %v\n", err)
		return media.ClassOf(err).ExitCode()
	}
	printResult(o, result)

	// Re-read the produced index to confirm every keyframe entry lands on a
	// real keyframe boundary in the regenerated stream.
	if err := verifyIndexBoundaries(o, corpus, result); err != nil {
		fmt.Fprintf(os.Stderr, "archivectl: verification failed: %v\n", err)
		return media.ClassCheckpoint.ExitCode()
	}
	if !verifyMetrics(o, result) {
		return media.ClassCheckpoint.ExitCode()
	}
	fmt.Printf("verify=PASS manifest_sha256=%s\n", result.ManifestSHA256)
	return 0
}

func verifyIndexBoundaries(o cliOptions, c *testkit.Corpus, r *job.Result) error {
	adapt, err := adapter.Latest(o.codec)
	if err != nil {
		return err
	}
	sp := adapt.NewSplitter()
	units, _, err := sp.Write(c.Stream)
	if err != nil {
		return err
	}
	tail, _ := sp.Close()
	keyOffsets := map[int64]bool{}
	for _, u := range append(units, tail...) {
		if u.IsKeyframe {
			keyOffsets[u.Offset] = true
		}
	}
	f, err := os.Open(r.IndexPath)
	if err != nil {
		return err
	}
	defer f.Close()
	li, err := index.ReadLegacyIndex(f)
	if err != nil {
		return err
	}
	hits := 0
	for _, e := range li.Entries {
		if e.IsKeyframe {
			if !keyOffsets[e.Offset] {
				return fmt.Errorf("indexed keyframe offset %d is not a keyframe", e.Offset)
			}
			hits++
		}
	}
	if hits == 0 {
		return fmt.Errorf("no keyframes present in rebuilt index")
	}
	return nil
}

// verifyMetrics enforces the acceptance ranges from the specification.
func verifyMetrics(o cliOptions, r *job.Result) bool {
	ok := true
	check := func(name string, pass bool) {
		if !pass {
			fmt.Fprintf(os.Stderr, "archivectl: metric %s outside accepted range\n", name)
			ok = false
		}
	}
	check("roundtrip_match_pct", r.RoundtripMatchPct >= 99.0 && r.RoundtripMatchPct <= 100.0)
	check("keyframe_hit_pct", r.KeyframeHitPct >= 98.0 && r.KeyframeHitPct <= 100.0)
	check("sha256_match", r.SHA256Match >= 0 && r.SHA256Match <= r.Chunks)
	check("manifest_sha256", len(r.ManifestSHA256) == 64)
	return ok
}

func printResult(o cliOptions, r *job.Result) {
	fmt.Printf("codec=%s adapter=%s seed=%d scenario=%s status=%s\n",
		o.codec, adapterVersion(o.codec), o.seed, o.scenario, r.Status)
	fmt.Printf("roundtrip_match_pct=%.1f keyframe_hit_pct=%.1f\n",
		r.RoundtripMatchPct, r.KeyframeHitPct)
	fmt.Printf("chunks=%d sha256_match=%d/%d repaired_timestamps=%d truncated_units=%d\n",
		r.Chunks, r.SHA256Match, r.Chunks, r.RepairedTimestamps, r.TruncatedUnits)
	fmt.Printf("resume_skipped_chunks=%d manifest_sha256=%s\n",
		r.ResumeSkippedChunks, r.ManifestSHA256)
	if r.ManifestPath != "" {
		fmt.Printf("manifest_path=%s index_path=%s\n", r.ManifestPath, r.IndexPath)
	}
}

func adapterVersion(codec string) string {
	a, err := adapter.Latest(codec)
	if err != nil {
		return "unknown"
	}
	return a.Version()
}
