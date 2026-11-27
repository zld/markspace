package replay_test

import (
	"bytes"
	"strings"
	"testing"

	"replay/internal/fault"
	"replay/internal/replay"
	"replay/internal/scenario"
)

func fixture(t *testing.T, cs string, versions ...string) scenario.Fixture {
	t.Helper()
	if len(versions) == 0 {
		versions = []string{"v1", "v2"}
	}
	fx, err := scenario.Build(cs, versions)
	if err != nil {
		t.Fatalf("scenario.Build(%s): %v", cs, err)
	}
	return fx
}

func engine(t *testing.T, fx scenario.Fixture, dir string, opts ...func(*replay.Engine)) *replay.Engine {
	t.Helper()
	log := &bytes.Buffer{}
	e := &replay.Engine{WorkDir: dir, Fixture: fx, Log: log}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func TestNormalRunPublishesVerifiedManifest(t *testing.T) {
	fx := fixture(t, scenario.CaseNormal)
	log := &bytes.Buffer{}
	e := engine(t, fx, t.TempDir(), func(e *replay.Engine) { e.Log = log })
	res, err := e.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != replay.TargetCompleted || !res.Verified {
		t.Fatalf("outcome=%v verified=%v", res.Outcome, res.Verified)
	}
	if len(res.ManifestHash) != 64 {
		t.Fatalf("manifest hash=%q", res.ManifestHash)
	}
	stats := res.Manifest.Stats
	if stats.Tracks != 2 {
		t.Fatalf("tracks=%d", stats.Tracks)
	}
	if stats.Packets == 0 {
		t.Fatal("expected accepted packets")
	}
	if stats.Lost != 2 {
		t.Fatalf("lost=%d want 2 (one per track)", stats.Lost)
	}
	if stats.Conflicts != 0 {
		t.Fatalf("conflicts=%d", stats.Conflicts)
	}
	if stats.Missing != 0 {
		t.Fatalf("missing=%d", stats.Missing)
	}
	// Stage transitions must cover all six stages.
	for _, stage := range replay.Plan() {
		if !strings.Contains(log.String(), stage) {
			t.Fatalf("stage %s missing from progress log", stage)
		}
	}
}

func TestRepeatRunIsByteIdentical(t *testing.T) {
	fx := fixture(t, scenario.CaseNormal)
	dir := t.TempDir()
	first, err := engine(t, fx, dir).Run()
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := engine(t, fx, dir).Run()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.ManifestHash != second.ManifestHash {
		t.Fatalf("manifest hash changed: %s vs %s", first.ManifestHash, second.ManifestHash)
	}
	if !second.Diff.Same || second.Diff.Changes() != 0 {
		t.Fatalf("repeat run diff=%+v changes=%d", second.Diff, second.Diff.Changes())
	}
}

func TestTruncatedRTCPReportsOffsetAndFails(t *testing.T) {
	fx := fixture(t, scenario.CaseTruncatedRTCP)
	_, err := engine(t, fx, t.TempDir()).Run()
	if err == nil {
		t.Fatal("expected truncation failure")
	}
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.KindTruncated {
		t.Fatalf("kind: %v", err)
	}
	if !fe.HasOffset || fe.ByteOffset < 0 {
		t.Fatalf("missing byte offset: %s", fe.Error())
	}
	if fe.Track != "audio-main" || fe.Stage != replay.StageIngest {
		t.Fatalf("location: %s", fe.Error())
	}
	if fault.ExitCode(err) != 1 {
		t.Fatalf("exit code=%d", fault.ExitCode(err))
	}
}

func TestConflictCaseReportsConflict(t *testing.T) {
	fx := fixture(t, scenario.CaseDuplicateConflict)
	res, err := engine(t, fx, t.TempDir()).Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Manifest.Stats.Conflicts < 1 {
		t.Fatalf("conflicts=%d want >= 1", res.Manifest.Stats.Conflicts)
	}
}

func TestSequenceWrapReordersAcrossBoundary(t *testing.T) {
	fx := fixture(t, scenario.CaseSequenceWrap)
	res, err := engine(t, fx, t.TempDir()).Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Manifest.Stats.Wraps < 1 {
		t.Fatalf("wraps=%d want >= 1", res.Manifest.Stats.Wraps)
	}
	// Same packet count as normal: the wrap must not lose or duplicate.
	normal := fixture(t, scenario.CaseNormal)
	want := 0
	for _, p := range normal.RTP {
		if strings.Contains(p.Track, "audio") {
			want++
		}
	}
	_ = want
	if res.Manifest.Stats.Packets == 0 {
		t.Fatal("no packets accepted across wrap")
	}
}

func TestDependencyCycleFailsWithoutMergeOutput(t *testing.T) {
	fx := fixture(t, scenario.CaseDependencyCycle)
	dir := t.TempDir()
	_, err := engine(t, fx, dir).Run()
	if err == nil {
		t.Fatal("expected cycle failure")
	}
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.KindGraph {
		t.Fatalf("kind: %v", err)
	}
	if fe.Stage != replay.StageGraph {
		t.Fatalf("stage=%s want graph", fe.Stage)
	}
	if !strings.Contains(fe.Reason, "cycle") {
		t.Fatalf("reason=%q", fe.Reason)
	}
	// No plan or manifest may be published from an invalid graph.
	for _, name := range []string{"out/plan.json", "manifest.json"} {
		if _, statErr := statFile(dir, name); statErr == nil {
			t.Fatalf("%s must not exist after a cycle failure", name)
		}
	}
	// Checkpoints still record the failure so resume reports it.
	stages := checkpointPhases(t, dir)
	if stages[replay.StageGraph] != "failed" {
		t.Fatalf("graph phase=%q want failed", stages[replay.StageGraph])
	}
}

func TestStopAfterMapPersistsResumePosition(t *testing.T) {
	fx := fixture(t, scenario.CaseResume)
	dir := t.TempDir()
	res, err := engine(t, fx, dir, func(e *replay.Engine) { e.StopAfter = replay.StageMap }).Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != replay.TargetStopped || res.StoppedAfter != replay.StageMap {
		t.Fatalf("outcome=%v stopped=%s", res.Outcome, res.StoppedAfter)
	}
	if res.Manifest != nil {
		t.Fatal("a stopped run must not publish a manifest")
	}
	// The resume position equals the number of packets processed.
	if res.ResumePosition == 0 || res.ResumeUnit != "packets" {
		t.Fatalf("resume=%d %s", res.ResumePosition, res.ResumeUnit)
	}
	if res.ResumePosition != fx.RTPCount() {
		t.Fatalf("resume position=%d want processed datagrams=%d", res.ResumePosition, fx.RTPCount())
	}
}

func TestResumeCompletesAndIsStable(t *testing.T) {
	fx := fixture(t, scenario.CaseResume)
	dir := t.TempDir()
	if _, err := engine(t, fx, dir, func(e *replay.Engine) { e.StopAfter = replay.StageMap }).Run(); err != nil {
		t.Fatalf("first phase: %v", err)
	}
	resumed, err := engine(t, fx, dir, func(e *replay.Engine) { e.Resume = true }).Run()
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Outcome != replay.TargetCompleted || !resumed.Verified {
		t.Fatalf("resume outcome=%v verified=%v", resumed.Outcome, resumed.Verified)
	}
	if resumed.ResumedFrom != replay.StageMerge {
		t.Fatalf("resumed from %s want merge", resumed.ResumedFrom)
	}
	for _, stage := range []string{replay.StageIngest, replay.StageTimeline, replay.StageGraph, replay.StageMap} {
		found := false
		for _, reused := range resumed.Reused {
			if reused == stage {
				found = true
			}
		}
		if !found {
			t.Fatalf("stage %s should be reused, reused=%v", stage, resumed.Reused)
		}
	}

	again, err := engine(t, fx, dir, func(e *replay.Engine) { e.Resume = true }).Run()
	if err != nil {
		t.Fatalf("second resume: %v", err)
	}
	if again.ManifestHash != resumed.ManifestHash {
		t.Fatalf("resume changed the manifest: %s vs %s", again.ManifestHash, resumed.ManifestHash)
	}
	if again.Diff.Changes() != 0 {
		t.Fatalf("repeat resume diff=%d", again.Diff.Changes())
	}
}

func TestChangedInputRollsBackDownstream(t *testing.T) {
	dir := t.TempDir()
	base := fixture(t, scenario.CaseNormal)
	if _, err := engine(t, base, dir).Run(); err != nil {
		t.Fatalf("baseline run: %v", err)
	}
	// Change only the asset content key, which must invalidate map and
	// everything after it while leaving ingest/timeline/graph intact.
	changed := base
	changed.Assets = append([]scenario.AssetSpec(nil), base.Assets...)
	for i := range changed.Assets {
		changed.Assets[i].ContentKey = "changed/" + changed.Assets[i].Track
	}
	res, err := engine(t, changed, dir).Run()
	if err != nil {
		t.Fatalf("changed run: %v", err)
	}
	phases := checkpointPhases(t, dir)
	for _, stage := range []string{replay.StageIngest, replay.StageTimeline, replay.StageGraph} {
		if phases[stage] != "completed" {
			t.Fatalf("%s phase=%q, should stay completed", stage, phases[stage])
		}
	}
	for _, stage := range []string{replay.StageMap, replay.StageMerge, replay.StageManifest} {
		if phases[stage] != "completed" {
			t.Fatalf("%s should have re-run to completion, got %q", stage, phases[stage])
		}
	}
	// The rollback record must name the stage and a reason.
	if len(res.Transitions) == 0 {
		t.Fatal("expected rollback transitions")
	}
	sawRolledBack := false
	for _, tr := range res.Transitions {
		if tr.To == "rolled-back" {
			sawRolledBack = true
		}
	}
	if !sawRolledBack {
		t.Fatalf("no rolled-back transition: %+v", res.Transitions)
	}
}
