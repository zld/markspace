package main

import (
	"fmt"
	"os"

	"replaykit/internal/fixture"
	"replaykit/media/audit"
	"replaykit/media/recovery"
	"replaykit/media/segment"
)

// runResume builds six segments, corrupts one output's bytes, then reruns and
// asserts exactly one rebuild, five reuses and zero duplicate products.
func runResume(root string) scenarioResult {
	const name = "resume"
	rep := audit.NewReport(name)
	dir := caseDir(root, name)

	hi, err := newLayer("720p", normalHigh(), 3000, nil)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "resume hi: "+err.Error())
	}
	lo, err := newLayer("360p", normalLow(), int64(fixture.LoFrameTicks), nil)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "resume lo: "+err.Error())
	}
	fan := segment.Fanout(proofEvents(), []*segment.Layer{hi, lo}, segment.Options{})
	if fan.Unrecoverable != 0 || fan.Segments != 6 {
		return finish(name, root, 0, 1, rep, false,
			fmt.Sprintf("resume planning segments=%d unrecoverable=%d", fan.Segments, fan.Unrecoverable))
	}
	items := toItems(fan.Windows)

	first, err := recovery.Run(dir, items)
	if err != nil || first.Built != 6 {
		return finish(name, root, 0, 1, rep, false,
			fmt.Sprintf("resume first run built=%d err=%v", first.Built, err))
	}
	fmt.Printf("[resume] first run built=%d reused=%d\n", first.Built, first.Reused)

	// Inject an output hash failure into exactly one canonical artifact.
	victim := items[2]
	victimPath := dir + "/segments/" + victim.File
	if err := os.WriteFile(victimPath, []byte("CORRUPT-OUTPUT-HASH-FAILURE"), 0o644); err != nil {
		return finish(name, root, 0, 1, rep, false, "inject: "+err.Error())
	}
	fmt.Printf("[resume] injected output hash failure: %s\n", victim.File)

	rerun, err := recovery.Run(dir, items)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "resume rerun: "+err.Error())
	}
	rep.AddAll(rerun.Anomalies)
	m := &rep.Metrics
	m.Segments = rerun.HashTotal
	m.Rebuilt, m.Reused = rerun.Rebuilt, rerun.Reused
	m.DuplicateProducts = rerun.DuplicateProducts
	m.HashMatched, m.HashTotal = rerun.HashMatched, rerun.HashTotal

	ok := rerun.Rebuilt == 1 && rerun.Reused == 5 && rerun.DuplicateProducts == 0 &&
		rerun.HashMatched == 6 && rerun.HashTotal == 6
	line := fmt.Sprintf("[resume] rerun: rebuilt=%d reused=%d duplicate_products=%d hash_match=%s %s",
		rerun.Rebuilt, rerun.Reused, rerun.DuplicateProducts,
		hashMatchFrac(rerun.HashMatched, rerun.HashTotal), boolOK(ok))
	code := 0
	if !ok {
		code = 1
	}
	return finish(name, root, 0, code, rep, ok, line)
}

// runVerifyFail proves strict verification reports an output hash failure
// without rebuilding, which the CLI maps to exit status 1.
func runVerifyFail(root string) scenarioResult {
	const name = "verifyfail"
	rep := audit.NewReport(name)
	dir := caseDir(root, name)

	hi, err := newLayer("720p", normalHigh(), 3000, nil)
	if err != nil {
		return finish(name, root, 1, 1, rep, false, "verifyfail layer: "+err.Error())
	}
	events := proofEvents()[:2]
	fan := segment.Fanout(events, []*segment.Layer{hi}, segment.Options{})
	items := toItems(fan.Windows)
	if _, err := recovery.Run(dir, items); err != nil {
		return finish(name, root, 1, 1, rep, false, "verifyfail setup: "+err.Error())
	}
	if err := os.WriteFile(dir+"/segments/"+items[0].File, []byte("bitrot"), 0o644); err != nil {
		return finish(name, root, 1, 1, rep, false, "verifyfail inject: "+err.Error())
	}
	matched, bad := recovery.Verify(dir, items)
	code := 0
	ok := matched == 1 && len(bad) == 1
	if !ok {
		code = 1 // the injected failure was not detected -> selftest itself fails
	} else {
		code = 1 // expected production exit status for output verification failure
	}
	line := fmt.Sprintf("[verifyfail] strict verify hash_match=%s bad=%d expected_exit=1 %s",
		hashMatchFrac(matched, len(items)), len(bad), boolOK(ok))
	// ExpectedCode is 1 and Code is 1 when the failure is correctly detected.
	return finish(name, root, 1, code, rep, ok, line)
}
