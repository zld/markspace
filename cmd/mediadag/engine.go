package main

import (
	"fmt"
	"io"
	"os"

	"mediadag/internal/gen"
	"mediadag/internal/ingest"
	"mediadag/internal/task"
)

// sample is one generated input plus its canonical parameters.
type sample struct {
	name    string
	s       *gen.Sample
	params  []byte
	reorder int  // effective reorder window the sample was built with
	strict  bool // truncation must surface as input-corrupt
}

type demo struct {
	args *demoArgs
	ckpt *task.Checkpoint
	out  io.Writer
	err  io.Writer
}

func (dm *demo) makeSample(name string) *sample {
	d := dm.args
	var p gen.Params
	strict := false
	switch name {
	case "clean":
		p = d.cleanParams()
	case "lossy":
		p = d.lossyParams()
	case "truncated":
		p = d.cleanParams()
		p.Truncate = true
		strict = true
	case "noanchor":
		p = d.lossyParams()
		p.NoRTCP = true
	}
	s := gen.Generate(p)
	return &sample{name: name, s: s, params: paramsJSONFor(s, p, d.targetKbps, d.segmentMs),
		reorder: p.ReorderWindow, strict: strict}
}

// runDemo parses flags, loads the checkpoint and dispatches to a scenario.
func runDemo(args []string) int {
	d, err := parseDemoArgs(args)
	if err != nil {
		writeError(os.Stderr, "%v", err)
		return exitUsage
	}
	dm := &demo{args: d, out: os.Stdout, err: os.Stderr}
	if d.checkpoint != "" {
		dm.ckpt, err = task.LoadCheckpoint(d.checkpoint)
		if err != nil {
			writeError(dm.err, "%v", err)
			return exitUnexp
		}
		if err := dm.ckpt.Save(); err != nil { // prove the path is writable
			writeError(dm.err, "%v", err)
			return exitUnexp
		}
	} else {
		dm.ckpt = &task.Checkpoint{Nodes: map[string]*task.NodeState{}}
	}
	if d.scenario == "all" {
		return dm.runAll()
	}
	return dm.runSingle(d.scenario)
}

// withoutRTCP copies the sample without RTCP sender reports, simulating a
// capture that lost its clock anchors.
func withoutRTCP(s *gen.Sample) *gen.Sample {
	out := &gen.Sample{Digest: s.Digest}
	for _, ev := range s.Events {
		if ev.Kind == ingest.KindRTCP {
			continue
		}
		out.Events = append(out.Events, ev)
	}
	return out
}

// run executes one sample through the DAG.
func (dm *demo) run(sm *sample) (*task.Report, error) {
	rep, err := runPipeline(dm.ckpt, sm.s, sm.params, sm.reorder,
		dm.args.targetKbps, dm.args.segmentMs, pipelineOptions{StrictNAL: sm.strict})
	if err != nil {
		writeError(dm.err, "%v", err)
	}
	return rep, err
}

// runSingle handles one named scenario; blocking scenarios exit 2.
func (dm *demo) runSingle(name string) int {
	sm := dm.makeSample(name)
	rep, err := dm.run(sm)
	if err != nil {
		return exitUnexp
	}
	dm.printHeader(name, sm.s)
	dm.printVersions()
	dm.printStates(rep)

	if rep.FirstBlocked != "" {
		fmt.Fprintf(dm.out, "run=blocked first_blocked_dependency=%s category=%s\n",
			rep.FirstBlocked, rep.BlockedCategory)
		return exitBlocked
	}
	if sm.strict {
		writeError(dm.err, "strict inspect expected to block on truncated NAL")
		return exitUnexp
	}
	sum := rep.Results["report"].(*Summary)
	dm.printSummary(sum, rep.Reused, "")
	if name == "lossy" && !acceptanceOK(sum) {
		writeError(dm.err, "acceptance thresholds not met")
		return exitUnexp
	}
	return exitOK
}

// runAll walks the four required samples plus crash/resume and fault
// injection, proving digest reproduction and minimal re-execution.
func (dm *demo) runAll() int {
	// 1. lossless baseline
	clean := dm.makeSample("clean")
	cleanRep, err := dm.run(clean)
	if err != nil {
		return exitUnexp
	}
	dm.printHeader("clean", clean.s)
	dm.printVersions()
	if cleanRep.FirstBlocked != "" {
		writeError(dm.err, "clean sample blocked at %s", cleanRep.FirstBlocked)
		return exitUnexp
	}
	cleanSum := cleanRep.Results["report"].(*Summary)
	fmt.Fprintf(dm.out, "clean: packets=%d loss=%d idr=%d align=%.3fms bitrate_dev=%.2f%%\n",
		cleanSum.PacketsReceived, cleanSum.LossDetected, cleanSum.IDRCount,
		cleanSum.ManifestAlignMs, cleanSum.BitrateDevPct)
	if cleanSum.LossDetected != 0 || cleanSum.CorruptRanges != 0 || cleanSum.BitrateDevPct > 8 {
		writeError(dm.err, "lossless baseline violated: %+v", cleanSum)
		return exitUnexp
	}

	// 2. loss + reorder + duplicates
	lossy := dm.makeSample("lossy")
	lossyRep, err := dm.run(lossy)
	if err != nil {
		return exitUnexp
	}
	dm.printHeader("lossy", lossy.s)
	if lossyRep.FirstBlocked != "" {
		writeError(dm.err, "lossy sample blocked at %s", lossyRep.FirstBlocked)
		return exitUnexp
	}
	lossySum := lossyRep.Results["report"].(*Summary)
	dm.printSummary(lossySum, lossyRep.Reused, "")
	if !acceptanceOK(lossySum) {
		writeError(dm.err, "acceptance thresholds not met")
		return exitUnexp
	}

	// 3. crash/resume with unchanged input: all nodes reused, stable digest
	resumed, err := dm.run(lossy)
	if err != nil {
		return exitUnexp
	}
	resumedSum := resumed.Results["report"].(*Summary)
	fmt.Fprintf(dm.out, "resume_reused_nodes=%d\n", resumed.Reused)
	if resumed.Reused < 1 || digestOfSummary(resumedSum) != digestOfSummary(lossySum) {
		writeError(dm.err, "resume did not reproduce result_sha256")
		return exitUnexp
	}
	fmt.Fprintln(dm.out, "resume_digest_reproduced=true")

	// 4. failure injection: strip RTCP anchors, observe the block, then
	// restore them. Ingest must be reused and the digest must reproduce.
	brokenSample := withoutRTCP(lossy.s)
	broken := &sample{name: "noanchor", s: brokenSample,
		params: paramsJSONFor(brokenSample, dm.args.lossyParams(),
			dm.args.targetKbps, dm.args.segmentMs),
		reorder: lossy.reorder}
	injected, err := dm.run(broken)
	if err != nil {
		return exitUnexp
	}
	dm.printHeader("noanchor", broken.s)
	fmt.Fprintf(dm.out, "run=injected first_blocked_dependency=%s category=%s\n",
		injected.FirstBlocked, injected.BlockedCategory)
	if injected.FirstBlocked != "timeline" || injected.BlockedCategory != task.CatNoAnchor {
		writeError(dm.err, "expected timeline/no-anchor block, got %q/%s",
			injected.FirstBlocked, injected.BlockedCategory)
		return exitUnexp
	}
	recovered, err := dm.run(lossy)
	if err != nil {
		return exitUnexp
	}
	fmt.Fprintf(dm.out, "run=recovered first_blocked_dependency= reuse=%d\n", recovered.Reused)
	if recovered.FirstBlocked != "" || recovered.Reused < 1 {
		writeError(dm.err, "anchor recovery did not resume from checkpoint")
		return exitUnexp
	}
	if digestOfSummary(recovered.Results["report"].(*Summary)) != digestOfSummary(lossySum) {
		writeError(dm.err, "recovered digest differs from uninterrupted run")
		return exitUnexp
	}
	fmt.Fprintln(dm.out, "anchor_recovery_digest_match=true")

	// 5. truncated NAL: strict inspect blocks; repairing the input lets the
	// DAG finish while unaffected upstream state is reused.
	trunc := dm.makeSample("truncated")
	truncRep, err := dm.run(trunc)
	if err != nil {
		return exitUnexp
	}
	dm.printHeader("truncated", trunc.s)
	fmt.Fprintf(dm.out, "run=injected first_blocked_dependency=%s category=%s\n",
		truncRep.FirstBlocked, truncRep.BlockedCategory)
	if truncRep.FirstBlocked != "inspect" || truncRep.BlockedCategory != task.CatInputCorrupt {
		writeError(dm.err, "expected inspect/input-corrupt block, got %q/%s",
			truncRep.FirstBlocked, truncRep.BlockedCategory)
		return exitUnexp
	}
	repair, err := dm.run(clean)
	if err != nil {
		return exitUnexp
	}
	fmt.Fprintf(dm.out, "run=recovered first_blocked_dependency= reuse=%d\n", repair.Reused)
	if repair.FirstBlocked != "" {
		writeError(dm.err, "truncation repair still blocked at %s", repair.FirstBlocked)
		return exitUnexp
	}
	fmt.Fprintln(dm.out, "truncation_repair_digest_match=true")

	if digestOfSummary(repair.Results["report"].(*Summary)) != digestOfSummary(cleanSum) {
		writeError(dm.err, "repair digest differs from baseline")
		return exitUnexp
	}
	fmt.Fprintln(dm.out, "demo=ok")
	return exitOK
}
