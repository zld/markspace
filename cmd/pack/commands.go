package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gamepack/internal/fixture"
	perr "gamepack/internal/errors"
	"gamepack/internal/manifest"
	"gamepack/internal/pipeline"
	"gamepack/internal/recovery"
	"gamepack/internal/stream"
	"gamepack/internal/txn"
)

// exitCode maps the unified error model to the documented process codes.
func exitCode(err error) int {
	if pipeline.IsSimulatedCrash(err) {
		// A simulated crash is surfaced distinctly; resume is the next step.
		fmt.Fprintln(os.Stderr, "interrupted:", err)
		return 4
	}
	pe := perr.AsError(err)
	if pe == nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "failure [%s] %s\n", pe.Code, pe.Error())
	switch pe.Code {
	case perr.CodeOffsetOutOfRange, perr.CodeTruncatedUnit, perr.CodeIndexMissing,
		perr.CodeInvalidEncoding, perr.CodeClockBackward, perr.CodeBoundaryConflict:
		// Input/boundary failures: no partial commit is allowed.
		return 2
	case perr.CodeChecksum:
		return 3
	default:
		return 1
	}
}

func runDemo(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}

	// A truncated injection exercises the negative scan path in an isolated
	// sandbox. When truncation is the ONLY injection the whole demo fails with
	// exit code 2 and commits nothing. When other injections are present, the
	// negative test is reported but the packable switch/wrap/crash job still
	// proceeds so a single combined --inject demonstrates both behaviors.
	if f.inject[fixture.InjectTruncated] {
		truncErr := runTruncatedSandbox(f)
		if truncErr != nil {
			return truncErr
		}
		if len(f.inject) == 1 {
			return perr.New("demo", perr.CodeTruncatedUnit,
				"truncated sample failed validation; job aborted with no partial commit")
		}
	}

	cfg := pipeline.Config{
		Root: f.root, Codecs: f.codecs, Bitrates: f.bitrates,
		Units: 12, KeyEvery: 4, WrapBits: 16,
		Inject: nonTruncated(f.inject), CrashAfter: f.crashAfter,
	}
	runner, err := pipeline.New(cfg)
	if err != nil {
		return err
	}
	rep, err := runner.Run()
	if err != nil {
		if pipeline.IsSimulatedCrash(err) {
			printCrashSummary(f.root, f.crashAfter)
		}
		return err
	}
	printReport(rep)
	return nil
}

func nonTruncated(in map[fixture.Injection]bool) map[fixture.Injection]bool {
	out := map[fixture.Injection]bool{}
	for k, v := range in {
		if k != fixture.InjectTruncated {
			out[k] = v
		}
	}
	return out
}

// runTruncatedSandbox generates one deliberately broken rendition in a
// throwaway directory and confirms the scanner rejects it; the main job root
// is never touched, so no fragment or manifest can be partially committed.
func runTruncatedSandbox(f *flags) error {
	sandbox := filepath.Join(f.root, "truncated-sandbox")
	codec := f.codecs[0]
	br := f.bitrates[0]
	art, err := fixture.Generate(sandbox, fixture.Options{
		Codec: codec, BitrateKbps: br, Units: 8, KeyEvery: 4, WrapBits: 16,
		Injections: map[fixture.Injection]bool{fixture.InjectTruncated: true},
	})
	if err != nil {
		return err
	}
	if _, err := stream.ScanFile(codec, art.TruncatedPath, art.TruncatedIndex); err != nil {
		pe := perr.AsError(err)
		fmt.Printf("[negative] truncated input rejected: codec=%s code=%s first_bad_byte=%d\n",
			codec, pe.Code, pe.Offset)
		if _, statErr := os.Stat(filepath.Join(f.root, "manifest.json")); statErr == nil {
			return perr.New("demo", perr.CodeTxn, "manifest present during truncated negative test")
		}
		fmt.Println("[negative] no partial commit; truncated sample is excluded from fragments")
		return nil
	}
	return perr.New("demo", perr.CodeStage, "truncated fixture unexpectedly scanned cleanly")
}

func runResume(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	// Resume reuses the stored job configuration by rerunning without a crash.
	plan, err := recovery.Inspect(filepath.Join(f.root, "txn"))
	if err != nil {
		return err
	}
	fmt.Printf("resume stage: %s (verified=%s, leftovers cleaned=%d, rollbacks=%d)\n",
		plan.ResumeStage, plan.VerifiedStage, plan.Leftovers, plan.Rollbacks)

	// Reconstruct the config from the existing inputs; no new injections.
	cfg, err := configFromRoot(f.root)
	if err != nil {
		return err
	}
	cfg.CrashAfter = ""
	runner, err := pipeline.New(cfg)
	if err != nil {
		return err
	}
	rep, err := runner.Run()
	if err != nil {
		return err
	}
	printReport(rep)
	return nil
}

func runVerify(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	path := filepath.Join(f.root, "manifest.json")
	body, err := os.ReadFile(path)
	if err != nil {
		return perr.New("verify", perr.CodeChecksum, "manifest not found: %v", err)
	}
	m, err := manifest.Unmarshal(body)
	if err != nil {
		return err
	}
	passed, failed := 0, 0
	if err := manifest.Verify(m); err != nil {
		failed++
		fmt.Println("manifest digest check: FAIL")
		return err
	}
	passed++
	fmt.Println("manifest digest check: PASS")

	for _, e := range m.Entries {
		for id, frag := range e.Segments {
			sum, err := hashRel(f.root, frag.Path)
			if err != nil {
				failed++
				fmt.Printf("fragment %s/%s: MISSING\n", e.ID, id)
				continue
			}
			if sum != frag.SHA256 {
				failed++
				fmt.Printf("fragment %s/%s: HASH MISMATCH\n", e.ID, id)
				continue
			}
			passed++
			fmt.Printf("fragment %s/%s: PASS (%s)\n", e.ID, id, short(sum))
		}
	}
	fmt.Printf("verify summary: %d passed, %d failed\n", passed, failed)
	if failed > 0 {
		return perr.New("verify", perr.CodeChecksum, "%d verification item(s) failed", failed)
	}
	// Two distinct hashes, both reported: the embedded content digest used for
	// tamper detection, and the SHA-256 of the serialized manifest file.
	fileSHA, err := hashRel(f.root, "manifest.json")
	if err != nil {
		return err
	}
	fmt.Printf("manifest content digest: %s\n", m.Digest)
	fmt.Printf("manifest file SHA-256: %s\n", fileSHA)
	return nil
}

func printReport(rep *pipeline.Report) {
	fmt.Println("stage transitions:")
	for _, rec := range rep.StageLog {
		fmt.Println("  ", rec.String())
	}
	fmt.Printf("resume stage: %s\n", rep.ResumeStage)
	fmt.Printf("manifest: %s\n", rep.ManifestPath)
	fmt.Printf("manifest SHA-256: %s\n", rep.ManifestSHA)
	fmt.Printf("entries: %d\n", rep.EntryCount)
	fmt.Printf("reused: %d  new: %d  changed: %d  (input changed=%v)\n",
		rep.Diff.ReusedCount, rep.Diff.NewCount, rep.Diff.ChangedCount,
		rep.Diff.InputChanged)
	fmt.Printf("rollbacks: %d\n", rep.Rollbacks)
	fmt.Printf("zero-tick cut points: %d/%d\n", rep.ZeroTickDiffs, rep.EntryCount)
}

func printCrashSummary(root string, stage txn.Stage) {
	plan, err := recovery.Inspect(filepath.Join(root, "txn"))
	if err == nil {
		fmt.Printf("simulated crash after %s; last verified=%s; resume from %s\n",
			stage, plan.VerifiedStage, plan.ResumeStage)
	} else {
		fmt.Printf("simulated crash after %s\n", stage)
	}
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12] + "..."
}
