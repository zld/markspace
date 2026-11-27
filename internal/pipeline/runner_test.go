package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"medtrace/internal/fixture"
	"medtrace/internal/mederr"
	"medtrace/internal/state"
)

func writeInputs(t *testing.T, dir string, webm []byte, events []byte) (string, string) {
	t.Helper()
	mp := filepath.Join(dir, "sample.webm")
	ep := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(mp, webm, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ep, events, 0o644); err != nil {
		t.Fatal(err)
	}
	return mp, ep
}

func TestRunSuccessAndThresholds(t *testing.T) {
	dir := t.TempDir()
	webm, _ := fixture.GenerateWebM(fixture.DefaultWebMConfig(7))
	recs, truth := fixture.GenerateEvents(fixture.DefaultEventConfig(7))
	mp, ep := writeInputs(t, dir, webm, fixture.MarshalEvents(recs))

	r := NewRunner(filepath.Join(dir, "state"), DefaultParams())
	rep, err := r.Run(mp, ep, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := rep.Validate(20); err != nil {
		t.Fatalf("thresholds: %v\n%s", err, rep)
	}
	if rep.AVSyncErrorMs > 20 {
		t.Fatalf("sync error %f > 20ms", rep.AVSyncErrorMs)
	}
	if rep.LossRecoveryRate < 0.8 || rep.LossRecoveryRate > 1.0 {
		t.Fatalf("recovery rate %f", rep.LossRecoveryRate)
	}
	if rep.P95LatencyMs < 0 {
		t.Fatalf("p95 must be non-negative, got %f", rep.P95LatencyMs)
	}
	if rep.DuplicatePackets != int64(truth.Duplicates) {
		t.Fatalf("duplicates=%d want %d", rep.DuplicatePackets, truth.Duplicates)
	}
	if rep.RecoveredPackets != int64(truth.Recovered) || rep.LostPackets != int64(truth.PermanentLoss) {
		t.Fatalf("recovered=%d lost=%d truth=%+v", rep.RecoveredPackets, rep.LostPackets, truth)
	}
	if len(rep.CompletedStages) != 4 {
		t.Fatalf("stages=%v", rep.CompletedStages)
	}
	if len(rep.InputSHA256) != 2 {
		t.Fatalf("input hashes=%v", rep.InputSHA256)
	}
	if len(rep.Offsets) != 1 || rep.Offsets[0].End <= rep.Offsets[0].Start {
		t.Fatalf("byte offsets not recorded: %+v", rep.Offsets)
	}
}

func TestResumeReusesStages(t *testing.T) {
	dir := t.TempDir()
	webm, _ := fixture.GenerateWebM(fixture.DefaultWebMConfig(7))
	recs, _ := fixture.GenerateEvents(fixture.DefaultEventConfig(7))
	mp, ep := writeInputs(t, dir, webm, fixture.MarshalEvents(recs))
	stateDir := filepath.Join(dir, "state")

	r := NewRunner(stateDir, DefaultParams())
	first, err := r.Run(mp, ep, false)
	if err != nil {
		t.Fatal(err)
	}
	// Second run: every stage must be reused, no growth.
	second, err := r.Run(mp, ep, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.CompletedStages) != len(first.CompletedStages) {
		t.Fatalf("stage count grew on resume: %v vs %v", first.CompletedStages, second.CompletedStages)
	}
	if absF(second.AVSyncErrorMs-first.AVSyncErrorMs) > 1e-9 ||
		absF(second.LossRecoveryRate-first.LossRecoveryRate) > 1e-9 {
		t.Fatal("resumed stats differ from first run")
	}
	// Provenance hashes must match fresh summaries.
	mpProv, err := state.SummarizeFile(mp)
	if err != nil {
		t.Fatal(err)
	}
	if second.InputSHA256[mp] != mpProv.SHA256 {
		t.Fatal("input sha256 not verifiable against file")
	}
}

func TestChangedInputInvalidates(t *testing.T) {
	dir := t.TempDir()
	webm, _ := fixture.GenerateWebM(fixture.DefaultWebMConfig(7))
	recs, _ := fixture.GenerateEvents(fixture.DefaultEventConfig(7))
	mp, ep := writeInputs(t, dir, webm, fixture.MarshalEvents(recs))
	stateDir := filepath.Join(dir, "state")

	r := NewRunner(stateDir, DefaultParams())
	if _, err := r.Run(mp, ep, false); err != nil {
		t.Fatal(err)
	}
	// Modify the media file: media/graph/sync stages must rerun, network kept.
	webm2, _ := fixture.GenerateWebM(fixture.WebMConfig{
		Seed: 99, Clusters: 14, TimecodeScale: 1_000_000,
	})
	if err := os.WriteFile(mp, webm2, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run(mp, ep, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.CompletedStages) != 4 {
		t.Fatalf("all four stages must exist after invalidation: %v", rep.CompletedStages)
	}
}

func TestCorruptMediaPreservesErrorLocation(t *testing.T) {
	dir := t.TempDir()
	cfg := fixture.DefaultWebMConfig(7)
	cfg.Corruption = fixture.CorruptInvalidVINT
	webm, _ := fixture.GenerateWebM(cfg)
	recs, _ := fixture.GenerateEvents(fixture.DefaultEventConfig(7))
	mp, ep := writeInputs(t, dir, webm, fixture.MarshalEvents(recs))

	r := NewRunner(filepath.Join(dir, "state"), DefaultParams())
	_, err := r.Run(mp, ep, false)
	se := mederr.AsError(err)
	if se == nil {
		t.Fatal("corrupt media must fail")
	}
	if se.Category != mederr.CatInvalidVINT || se.Offset < 0 || len(se.Path) == 0 {
		t.Fatalf("error must keep category, offset and path: %v", se)
	}
}

func TestCorruptUnknownLengthPreservesOffset(t *testing.T) {
	dir := t.TempDir()
	cfg := fixture.DefaultWebMConfig(7)
	cfg.Corruption = fixture.CorruptUnknownLen
	webm, _ := fixture.GenerateWebM(cfg)
	recs, _ := fixture.GenerateEvents(fixture.DefaultEventConfig(7))
	mp, ep := writeInputs(t, dir, webm, fixture.MarshalEvents(recs))

	r := NewRunner(filepath.Join(dir, "state"), DefaultParams())
	_, err := r.Run(mp, ep, false)
	se := mederr.AsError(err)
	if se == nil {
		t.Fatal("truncated unknown-length media must fail")
	}
	if se.Offset < 0 {
		t.Fatalf("error must carry byte offset: %v", se)
	}
}

func TestCorruptStateRecoverable(t *testing.T) {
	dir := t.TempDir()
	webm, _ := fixture.GenerateWebM(fixture.DefaultWebMConfig(7))
	recs, _ := fixture.GenerateEvents(fixture.DefaultEventConfig(7))
	mp, ep := writeInputs(t, dir, webm, fixture.MarshalEvents(recs))
	stateDir := filepath.Join(dir, "state")

	r := NewRunner(stateDir, DefaultParams())
	if _, err := r.Run(mp, ep, false); err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(stateDir, "checkpoint.json")
	raw, err := os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	raw[30] ^= 0xFF
	if err := os.WriteFile(cp, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// Without recovery allowance: state_corrupt propagates.
	if _, err := r.Run(mp, ep, false); mederr.AsError(err) == nil ||
		mederr.AsError(err).Category != mederr.CatStateCorrupt {
		t.Fatalf("want state_corrupt, got %v", err)
	}
	// With recovery: reset then complete successfully.
	rep, err := r.Run(mp, ep, true)
	if err != nil {
		t.Fatalf("recovery run: %v", err)
	}
	if len(rep.CompletedStages) != 4 {
		t.Fatalf("recovered run must rebuild all stages: %v", rep.CompletedStages)
	}
}

func TestCycleInputFailsWithoutSuccessState(t *testing.T) {
	metas := []TrackMeta{
		{ID: 1, Kind: 1, Codec: "V_VP9", DependsOn: []uint64{2}},
		{ID: 2, Kind: 1, Codec: "V_VP9", DependsOn: []uint64{1}},
	}
	_, err := buildOrder(metas)
	se := mederr.AsError(err)
	if se == nil || se.Category != mederr.CatCycle {
		t.Fatalf("cyclic metas must yield dependency_cycle, got %v", err)
	}
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
