package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediacore/internal/fixture"
	"mediacore/internal/merge"
	"mediacore/internal/model"
	"mediacore/internal/task"
)

func demoCmd(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", "", "output directory")
	seed := fs.Int64("seed", 17, "fixture seed")
	threshold := fs.Int64("threshold", 45000, "drift threshold in unified ticks")
	tolerance := fs.Int64("tolerance", 45, "time deviation tolerance in unified ticks")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *out == "" {
		usage()
		return exitUsage
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsage
	}
	samples, err := fixture.Generate(filepath.Join(*out, "fixtures"), *seed)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitProcessing
	}
	emit(map[string]interface{}{"phase": "fixtures", "seed": *seed, "samples": samples})

	storeDir := filepath.Join(*out, "store")
	outDir := filepath.Join(*out, "out")
	ok := true
	var results []merge.TaskResult
	for _, s := range samples {
		p := newPipeline(storeDir, outDir, *threshold, *tolerance)
		res, perr := runOne(p, s.Name, s.Path, s.Kind)
		if perr != nil {
			printFailure(s.Name, perr)
			if s.ExpectOK {
				ok = false
			}
			continue
		}
		printSuccess(res, defaultRules(tracksOf(p, res)))
		if !res.Report.OK {
			ok = false
		}
		if !s.ExpectOK {
			ok = false
			emit(map[string]interface{}{"sample": s.Name, "status": "unexpected_success", "exit_code": exitProcessing})
			continue
		}
		results = append(results, merge.TaskResult{TaskKey: res.TaskKey, SampleID: s.Name, Events: res.Events})
	}

	// Recovery: interrupt after map, resume, compare with continuous run.
	ok = demoRecovery(*out, *threshold, *tolerance, samples) && ok

	// Global deterministic manifest, built twice.
	m1 := merge.Build(results)
	m2 := merge.Build(append([]merge.TaskResult{}, results...))
	emit(map[string]interface{}{
		"summary": map[string]interface{}{
			"tasks":           len(results),
			"manifest_digest": m1.Digest,
			"repeat_digest":   m2.Digest,
			"deterministic":   m1.Digest == m2.Digest,
			"exit_code":       exitOK,
		},
	})
	if m1.Digest != m2.Digest {
		ok = false
	}
	if !ok {
		return exitProcessing
	}
	return exitOK
}

// demoRecovery runs the interrupt/resume and corrupt-checkpoint scenarios
// on the normal sample. Returns false on any mismatch.
func demoRecovery(out string, threshold, tolerance int64, samples []fixture.Sample) bool {
	var normal *fixture.Sample
	for i := range samples {
		if samples[i].Name == "normal" {
			normal = &samples[i]
		}
	}
	if normal == nil {
		return false
	}
	// Continuous reference run in a clean store.
	contDir := filepath.Join(out, "recovery-cont")
	pc := newPipeline(filepath.Join(contDir, "store"), filepath.Join(contDir, "out"), threshold, tolerance)
	contRes, err := pc.Run(normal.Name, normal.Path, normal.Kind)
	if err != nil {
		emit(map[string]interface{}{"recovery": "continuous run failed", "error": errToJSON(model.AsError(err, model.StageDemux))})
		return false
	}
	contDigest := merge.Build([]merge.TaskResult{{TaskKey: contRes.TaskKey, SampleID: contRes.SampleID, Events: contRes.Events}}).Digest

	// Interrupted run: stop after the map stage.
	recDir := filepath.Join(out, "recovery")
	pr := newPipeline(filepath.Join(recDir, "store"), filepath.Join(recDir, "out"), threshold, tolerance)
	pr.StopAfter = model.StageMap
	_, err = pr.Run(normal.Name, normal.Path, normal.Kind)
	interrupted := err == task.ErrInterrupted
	// Resume with the same store.
	pr2 := newPipeline(filepath.Join(recDir, "store"), filepath.Join(recDir, "out"), threshold, tolerance)
	recRes, err := pr2.Run(normal.Name, normal.Path, normal.Kind)
	if err != nil {
		emit(map[string]interface{}{"recovery": "resume failed", "error": errToJSON(model.AsError(err, model.StageDemux))})
		return false
	}
	recDigest := merge.Build([]merge.TaskResult{{TaskKey: recRes.TaskKey, SampleID: recRes.SampleID, Events: recRes.Events}}).Digest
	var resumed []string
	for _, n := range recRes.Notes {
		if n.Status == "resumed" {
			resumed = append(resumed, string(n.Stage))
		}
	}
	match := contDigest == recDigest
	emit(map[string]interface{}{
		"recovery": map[string]interface{}{
			"interrupted_after":          string(model.StageMap),
			"interrupted":                interrupted,
			"resumed_stages":             resumed,
			"manifest_digest_continuous": contDigest,
			"manifest_digest_recovered":  recDigest,
			"match":                      match,
			"exit_code":                  exitOK,
		},
	})

	// Corrupt the map-stage payload, rerun: stage must be redone,
	// digest must still match the continuous run.
	key := recRes.TaskKey
	binPath := filepath.Join(recDir, "store", key, "map.bin")
	raw, rerr := os.ReadFile(binPath)
	corruptOK := false
	redoDigest := ""
	if rerr == nil && len(raw) > 4 {
		raw[3] ^= 0xff
		if os.WriteFile(binPath, raw, 0o644) == nil {
			pr3 := newPipeline(filepath.Join(recDir, "store"), filepath.Join(recDir, "out"), threshold, tolerance)
			res3, err3 := pr3.Run(normal.Name, normal.Path, normal.Kind)
			if err3 == nil {
				redoDigest = merge.Build([]merge.TaskResult{{TaskKey: res3.TaskKey, SampleID: res3.SampleID, Events: res3.Events}}).Digest
				for _, n := range res3.Notes {
					if n.Stage == model.StageMap && n.Status == "redone" {
						corruptOK = true
					}
				}
			}
		}
	}
	corruptMatch := corruptOK && redoDigest == contDigest
	emit(map[string]interface{}{
		"checkpoint_recovery": map[string]interface{}{
			"corrupted_stage":  string(model.StageMap),
			"redone":           corruptOK,
			"manifest_digest":  redoDigest,
			"reference_digest": contDigest,
			"match":            corruptMatch,
			"exit_code":        exitOK,
		},
	})
	return interrupted && match && corruptMatch
}
