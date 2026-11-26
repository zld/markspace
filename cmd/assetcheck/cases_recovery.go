package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"assetcheck/internal/fixture"
	"assetcheck/internal/media"
	"assetcheck/internal/task"
)

func demoCrashResume(out io.Writer, dir string, redeliver int) error {
	opts := fixture.DefaultOptions()
	source := filepath.Join(dir, "crash.mkv")
	refWork := filepath.Join(dir, "reference-work")
	crashWork := filepath.Join(dir, "crash-work")
	if err := fixture.WriteFile(source, opts); err != nil {
		return err
	}
	in := demoInput(source, opts.AssetID, "")
	ref, err := task.NewRunner(refWork).Process(in)
	if err != nil {
		return err
	}

	crashIn := in
	crashIn.CrashAfter = task.StageParsed
	if _, err := task.NewRunner(crashWork).Process(crashIn); !errors.Is(err, task.ErrInterrupted) {
		return media.SpecMismatch("DEMO_CRASH_NOT_INJECTED", "expected injected checkpoint interruption").Wrap(err)
	}

	resume, err := task.NewRunner(crashWork).Process(in)
	if err != nil {
		return err
	}
	if resume.ResumeFrom != task.StageParsed {
		return media.SpecMismatch("DEMO_WRONG_RESUME_STAGE", "resume did not start from parsed checkpoint").
			WithDetail("resume_from=" + string(resume.ResumeFrom))
	}
	results, err := deliverConcurrent(crashWork, in, redeliver)
	if err != nil {
		return err
	}
	all := append([]*task.Result{resume}, results...)
	if err := verifyResults(results); err != nil {
		return err
	}
	if resume.ManifestSHA256 != ref.ManifestSHA256 || !sameManifestSHA(all) {
		return media.SpecMismatch("RESUME_DIGEST_MISMATCH", "resumed manifest differs from uninterrupted run")
	}
	fmt.Fprintf(out, "resume_from=%s\n", resume.ResumeFrom)
	printCoreMetrics(out, resume)
	fmt.Fprintf(out, "reference_sha256=%s\n", ref.ManifestSHA256)
	printManifest(out, resume)
	return nil
}

func demoUnknownEOF(out io.Writer, dir string, redeliver int) error {
	opts := fixture.DefaultOptions()
	opts.UnknownSegment = true
	source := filepath.Join(dir, "unknown-size-eof.mkv")
	work := filepath.Join(dir, "unknown-work")
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
	fmt.Fprintln(out, "unknown_size_eof=true")
	fmt.Fprintf(out, "index_rebuilt=%t\n", primary.IndexRebuilt)
	fmt.Fprintf(out, "cache_hit=%t\n", primary.CacheHit)
	printCoreMetrics(out, primary)
	printManifest(out, primary)
	return nil
}

func demoInvalidVINT(dir string) error {
	opts := fixture.DefaultOptions()
	opts.InvalidVINT = true
	source := filepath.Join(dir, "invalid-vint.mkv")
	if err := fixture.WriteFile(source, opts); err != nil {
		return err
	}
	_, err := task.NewRunner(filepath.Join(dir, "corrupt-work")).Process(demoInput(source, opts.AssetID, ""))
	if err == nil {
		return media.SpecMismatch("INVALID_VINT_NOT_DETECTED", "illegal VINT was accepted")
	}
	return err
}

func demoTruncateCluster(dir string) error {
	opts := fixture.DefaultOptions()
	source := filepath.Join(dir, "truncate-cluster.mkv")
	if err := fixture.WriteFile(source, opts); err != nil {
		return err
	}
	if err := fixture.TruncateCluster(source); err != nil {
		return err
	}
	_, err := task.NewRunner(filepath.Join(dir, "corrupt-work")).Process(demoInput(source, opts.AssetID, ""))
	if err == nil {
		return media.SpecMismatch("TRUNCATED_CLUSTER_NOT_DETECTED", "truncated cluster was accepted")
	}
	return err
}
