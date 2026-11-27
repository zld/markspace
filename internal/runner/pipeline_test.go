package runner_test

import (
	"path/filepath"
	"testing"

	"mediasync/internal/fixture"
	"mediasync/internal/model"
	"mediasync/internal/runner"
	"mediasync/internal/rtp"
)

// buildWorld writes one synthetic container and its RTP event log into dir.
func buildWorld(t *testing.T, dir string, params runner.Params, scenario fixture.RTPScenario) (string, string) {
	t.Helper()
	media := filepath.Join(dir, "scene.mp4")
	specs := []fixture.TrackSpec{
		{Handler: "soun", TrackID: 1, Timescale: 1000,
			Deltas: []uint32{100, 100, 100, 100, 100, 100, 100, 100}},
		{Handler: "subt", TrackID: 2, Timescale: 1000,
			Deltas: []uint32{100, 100, 100, 100, 100, 100, 100, 100}},
	}
	if _, err := fixture.WriteMP4(media, 1000, specs); err != nil {
		t.Fatal(err)
	}
	stream := fixture.BuildRTP(scenario)
	events := filepath.Join(dir, "events.jsonl")
	arrivals := make([]model.Ticks, len(stream.Arrivals))
	for i, a := range stream.Arrivals {
		arrivals[i] = model.Ticks(a)
	}
	if err := rtp.WriteEventLog(events, arrivals, stream.Datagrams); err != nil {
		t.Fatal(err)
	}
	return media, events
}

func baseScenario() fixture.RTPScenario {
	return fixture.RTPScenario{
		SSRC: 4242, ClockHz: 90000, PacketCount: 40, StartSeq: 100,
	}
}

func TestPipelineCleanRun(t *testing.T) {
	dir := t.TempDir()
	params := runner.DefaultParams()
	media, events := buildWorld(t, dir, params, baseScenario())

	pl, err := runner.NewPipeline(filepath.Join(dir, "run"), params, "")
	if err != nil {
		t.Fatal(err)
	}
	// Task key needs the real input digest; recompute via a scan first.
	res, idxDigest, err := scanForDigest(t, media, params)
	if err != nil {
		t.Fatal(err)
	}
	pl, err = runner.NewPipeline(filepath.Join(dir, "run"), params, res.InputSHA256)
	if err != nil {
		t.Fatal(err)
	}
	out, manifest, err := pl.Run(media, events, nil)
	if err != nil {
		t.Fatalf("clean run: %v", err)
	}
	if out.Duplicates != 0 || out.Missing != 0 {
		t.Fatalf("clean stream anomalies: %+v", out)
	}
	if out.LossRecovery != 1.0 {
		t.Fatalf("no-loss recovery rate must be 1.0, got %f", out.LossRecovery)
	}
	if out.SyncErrorNS > model.Milli {
		t.Fatalf("synthetic sync error %d exceeds 1ms", out.SyncErrorNS)
	}
	if out.P95LatencyMS > 100 {
		t.Fatalf("P95 latency %f exceeds 100ms", out.P95LatencyMS)
	}
	if out.InputSHA256 == "" || idxDigest == "" {
		t.Fatal("input/index digests must be populated")
	}
	resultSHA, err := runner.WriteResult(filepath.Join(dir, "run"), out)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.AttachResult(filepath.Join(dir, "run"), runner.ResultName); err != nil {
		t.Fatal(err)
	}
	if manifest.ResultSHA256 != resultSHA {
		t.Fatal("manifest result digest must equal written digest")
	}
}

func TestPipelineInterruptAndResumeIdempotent(t *testing.T) {
	dir := t.TempDir()
	params := runner.DefaultParams()
	sc := baseScenario()
	media, events := buildWorld(t, dir, params, sc)
	res, _, err := scanForDigest(t, media, params)
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(dir, "run")

	// First run: interrupt after 2 committed checkpoints.
	pl1, _ := runner.NewPipeline(runDir, params, res.InputSHA256)
	pl1.SetInterrupt(2)
	if _, _, err := pl1.Run(media, events, nil); err != runner.InterruptError {
		t.Fatalf("want interrupt, got %v", err)
	}

	// Resume to completion.
	pl2, _ := runner.NewPipeline(runDir, params, res.InputSHA256)
	out1, _, err := pl2.Run(media, events, nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !out1.Resumed {
		t.Fatal("resumed run must be flagged")
	}
	sha1, err := runner.WriteResult(runDir, out1)
	if err != nil {
		t.Fatal(err)
	}

	// A second resume on the completed task must produce identical counters.
	pl3, _ := runner.NewPipeline(runDir, params, res.InputSHA256)
	out2, _, err := pl3.Run(media, events, nil)
	if err != nil {
		t.Fatalf("re-resume: %v", err)
	}
	sha2, err := runner.WriteResult(runDir, out2)
	if err != nil {
		t.Fatal(err)
	}
	if out1.Committed != out2.Committed || out1.Duplicates != out2.Duplicates ||
		out1.Missing != out2.Missing {
		t.Fatalf("retry double-counted: %+v vs %+v", out1, out2)
	}
	if sha1 != sha2 {
		t.Fatalf("result digests must match across retries:\n%s\n%s", sha1, sha2)
	}
}

func TestPipelineLossRecoveryRate(t *testing.T) {
	dir := t.TempDir()
	params := runner.DefaultParams()
	params.ReorderWindow = 8
	params.InjectedLoss = 4
	sc := baseScenario()
	// Two packets refill inside the window; two are lost forever.
	sc.LateSeqs = map[uint16]bool{102: true, 103: true}
	sc.LateBy = 3
	sc.DropSeqs = map[uint16]bool{110: true, 111: true}
	media, events := buildWorld(t, dir, params, sc)
	res, _, err := scanForDigest(t, media, params)
	if err != nil {
		t.Fatal(err)
	}
	pl, _ := runner.NewPipeline(filepath.Join(dir, "run"), params, res.InputSHA256)
	out, _, err := pl.Run(media, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Unrecovered != 2 {
		t.Fatalf("want 2 unrecovered outside window, got %d", out.Unrecovered)
	}
	if out.LossRecovery != 0.5 {
		t.Fatalf("recovery = %f, want 0.5 (2 refilled / 4 injected)", out.LossRecovery)
	}
}
