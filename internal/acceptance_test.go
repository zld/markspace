package rtpreplay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func execute(t *testing.T, opts RunOptions) RunOutcome {
	t.Helper()
	return Execute(opts)
}

func TestAcceptanceNormalDeterministic(t *testing.T) {
	a := execute(t, RunOptions{Case: CaseNormal, Seed: 17, Depth: 4})
	b := execute(t, RunOptions{Case: CaseNormal, Seed: 17, Depth: 4})
	if a.ExitCode != 0 || b.ExitCode != 0 {
		t.Fatalf("exit=%d/%d err=%v", a.ExitCode, b.ExitCode, a.Err)
	}
	if a.Report.ArtifactSHA256 != b.Report.ArtifactSHA256 {
		t.Fatalf("artifact digest drift: %s vs %s", a.Report.ArtifactSHA256, b.Report.ArtifactSHA256)
	}
	if a.Report.InputDigest != b.Report.InputDigest {
		t.Fatal("input digest drift")
	}
	if a.Report.Manifest.AlignmentErrorMs != 0 {
		t.Fatalf("alignment=%v", a.Report.Manifest.AlignmentErrorMs)
	}
	m := a.Report.Metrics[0]
	if m.LossPackets != 0 || m.DuplicatePackets != 0 || m.ReorderedPackets != 0 {
		t.Fatalf("unexpected anomalies: %+v", m)
	}
	if absFloat(m.Bitrate.DeviationPct) > 1 {
		t.Fatalf("deviation=%v", m.Bitrate.DeviationPct)
	}
	if m.QualityScore != 100 {
		t.Fatalf("quality=%v", m.QualityScore)
	}
	for _, field := range []string{
		"target_bitrate_bps=", "actual_bitrate_bps=", "bitrate_deviation_pct=",
		"quality_score=", "loss_packets=", "duplicate_packets=",
		"latency_p50_ms=", "manifest_alignment_error_ms=", "artifact_sha256=",
	} {
		if !strings.Contains(a.Summary, field) {
			t.Fatalf("summary missing %q:\n%s", field, a.Summary)
		}
	}
}

func TestAcceptanceReportWriteIsStable(t *testing.T) {
	dir := t.TempDir()
	a := execute(t, RunOptions{Case: CaseNormal, Seed: 17, Depth: 4})
	p1 := filepath.Join(dir, "a.json")
	p2 := filepath.Join(dir, "b.json")
	if err := WriteReport(p1, a.Report); err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(p2, a.Report); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(p1)
	b2, _ := os.ReadFile(p2)
	if string(b1) != string(b2) {
		t.Fatal("report bytes differ across writes")
	}
	var decoded Report
	if err := json.Unmarshal(b1, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ArtifactSHA256 != a.Report.ArtifactSHA256 {
		t.Fatal("artifact digest not persisted")
	}
	if decoded.Units.Bitrate != UnitBitsPerSec || decoded.Units.Latency != UnitMilliseconds {
		t.Fatalf("units=%+v", decoded.Units)
	}
}

func TestAcceptanceWrapReorderNoLoss(t *testing.T) {
	out := execute(t, RunOptions{Case: CaseWrapReorder, Seed: 17, Depth: 4})
	if out.ExitCode != 0 {
		t.Fatalf("exit=%d err=%v", out.ExitCode, out.Err)
	}
	m := out.Report.Metrics[0]
	if m.LossPackets != 0 || m.ReorderedPackets != 3 {
		t.Fatalf("loss=%d reorder=%d", m.LossPackets, m.ReorderedPackets)
	}
	e := out.Report.Manifest.Entries[0]
	if e.FirstExtSeq != 65516 || e.LastExtSeq != 65555 {
		t.Fatalf("seq range=%d..%d", e.FirstExtSeq, e.LastExtSeq)
	}
	if e.AlignmentErrorMs != 0 {
		t.Fatalf("alignment=%v", e.AlignmentErrorMs)
	}
}

func TestAcceptanceDuplicateDoesNotDoubleCountTime(t *testing.T) {
	out := execute(t, RunOptions{Case: CaseDuplicate, Seed: 17, Depth: 4})
	if out.ExitCode != 0 {
		t.Fatalf("exit=%d", out.ExitCode)
	}
	m := out.Report.Metrics[0]
	if m.DuplicatePackets != 4 || m.LossPackets != 0 {
		t.Fatalf("dup=%d loss=%d", m.DuplicatePackets, m.LossPackets)
	}
	if m.MediaDurationMs != 600 {
		t.Fatalf("media=%v (duplicates must not extend the timeline)", m.MediaDurationMs)
	}
}

// TestAcceptanceBurstLossPropagatesToMetricsAndManifest is requirement 7's
// first end-to-end propagation: injection at the arrival layer must surface as
// loss counts, burst segments, and manifest gaps, and must recover only through
// reordering depth, not by fabricating audio.
func TestAcceptanceBurstLossPropagatesToMetricsAndManifest(t *testing.T) {
	out := execute(t, RunOptions{Case: CaseBurstLoss, Seed: 17, Depth: 4})
	if out.ExitCode != 0 {
		t.Fatalf("exit=%d err=%v", out.ExitCode, out.Err)
	}
	m := out.Report.Metrics[0]
	if m.LossPackets != 12 {
		t.Fatalf("loss=%d", m.LossPackets)
	}
	if len(m.BurstLossRuns) != 2 {
		t.Fatalf("runs=%+v", m.BurstLossRuns)
	}
	if m.BurstLossRuns[0].Count != 8 || m.BurstLossRuns[1].Count != 4 {
		t.Fatalf("runs=%+v", m.BurstLossRuns)
	}
	e := out.Report.Manifest.Entries[0]
	if e.Status != StatusGap || e.GapCount != 12 || len(e.Gaps) != 2 {
		t.Fatalf("entry=%+v", e)
	}
	if e.Gaps[0].StartMs != 200 || e.Gaps[0].EndMs != 360 {
		t.Fatalf("gap window=%+v", e.Gaps[0])
	}
	if e.AlignmentErrorMs != 0 {
		t.Fatalf("alignment=%v (loss must not shift media boundaries)", e.AlignmentErrorMs)
	}
	if m.QualityScore >= 100 {
		t.Fatalf("quality=%v should be penalised", m.QualityScore)
	}
}

// TestAcceptanceMalformedOpusFailsStructured is requirement 7's second
// propagation: a bad codec header must fail the run with a structured error
// carrying track and packet sequence, and must not produce a success status.
func TestAcceptanceMalformedOpusFailsStructured(t *testing.T) {
	out := execute(t, RunOptions{Case: CaseMalformedOpus, Seed: 17, Depth: 4})
	if out.ExitCode != 1 {
		t.Fatalf("exit=%d", out.ExitCode)
	}
	se := AsError(out.Err)
	if se.Code != CodeDuration || se.PacketSeq == nil || *se.PacketSeq != 13 {
		t.Fatalf("err=%+v", se)
	}
	if out.Report == nil || out.Report.Status != "failed" {
		t.Fatalf("report=%+v", out.Report)
	}
	if out.Report.Blocked == nil || out.Report.Blocked.Node != "states" {
		t.Fatalf("blocker=%+v", out.Report.Blocked)
	}
	if !strings.Contains(out.Summary, "status=failed") {
		t.Fatalf("summary claims success:\n%s", out.Summary)
	}
}

func TestAcceptanceTimestampJumpFails(t *testing.T) {
	out := execute(t, RunOptions{Case: CaseTimestampJump, Seed: 17, Depth: 4})
	if out.ExitCode != 1 {
		t.Fatalf("exit=%d", out.ExitCode)
	}
	if AsError(out.Err).Code != CodeTimestampJump {
		t.Fatalf("err=%v", out.Err)
	}
}

func TestAcceptanceBlockedDagFirstBlocker(t *testing.T) {
	out := execute(t, RunOptions{Case: CaseBlockedDag, Seed: 17, Depth: 4})
	if out.ExitCode != 1 {
		t.Fatalf("exit=%d", out.ExitCode)
	}
	if out.Report.Status != "failed" || out.Report.Blocked == nil {
		t.Fatalf("report=%+v", out.Report)
	}
	if out.Report.Blocked.Node != "metrics" {
		t.Fatalf("blocker=%s", out.Report.Blocked.Node)
	}
	if !strings.Contains(out.Summary, "first_blocker=metrics") {
		t.Fatalf("summary:\n%s", out.Summary)
	}
	if strings.Contains(out.Summary, "report_status=ok") {
		t.Fatal("blocked run must not print a success status")
	}
	// Parse ran, states ran, metrics failed, report never produced.
	states := map[NodeID]string{}
	for _, r := range out.Report.Nodes {
		states[r.Node] = r.State
	}
	if states["parse"] != StateSucceeded || states["metrics"] != StateFailed || states["report"] != StateBlocked {
		t.Fatalf("states=%v", states)
	}
}

// TestAcceptanceTransientFaultRecoversThroughRetry proves the retry path is
// real: a retryable injected fault on metrics clears on the second attempt and
// the run completes.
func TestAcceptanceTransientFaultRecoversThroughRetry(t *testing.T) {
	fx, err := Generate(CaseNormal, 17)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := ParseTrace(mustJSON(t, fx.Trace))
	if err != nil {
		t.Fatal(err)
	}
	mp4, err := ReadMP4Tracks(fx.MP4Bytes)
	if err != nil {
		t.Fatal(err)
	}
	cfg := PipelineConfig{
		ReorderDepth: 4, Case: CaseNormal, Seed: 17, Version: "1",
		FailNode: "metrics", FaultMode: "transient", FaultTimes: 1,
	}
	p := &Pipeline{Trace: tr, Config: cfg, MP4: mp4}
	d, err := p.BuildDag()
	if err != nil {
		t.Fatal(err)
	}
	res := NewWorker(cfg.Digest()).Run(d)
	if !res.Completed {
		t.Fatalf("transient fault did not recover: %+v", res.FirstBlocker)
	}
	var metricsResult *NodeResult
	for _, r := range res.Results {
		if r.Node == "metrics" {
			metricsResult = r
		}
	}
	if metricsResult == nil || metricsResult.Attempts != 2 {
		t.Fatalf("metrics attempts=%v", metricsResult)
	}
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := marshalCanonical(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
