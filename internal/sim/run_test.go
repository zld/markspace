package sim

import "testing"

func testCfg() Config {
	return Config{Seed: 42, DurationS: 20, LossBurst: 3, DriftPPM: 120, ChunkMS: 20}
}

func TestRunMetricsPassThresholds(t *testing.T) {
	rep, err := Run(testCfg())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != statusOK {
		t.Fatalf("status=%s report=%+v", rep.Status, rep)
	}
	if rep.SyncErrorMSP95 > 20 {
		t.Fatalf("sync p95 %.3f ms > 20", rep.SyncErrorMSP95)
	}
	if rep.PacketRecoveryRate < 0.99 {
		t.Fatalf("recovery rate %.4f < 0.99", rep.PacketRecoveryRate)
	}
	if rep.PipelineLatencyMSP95 > 50 {
		t.Fatalf("latency p95 %.3f ms > 50", rep.PipelineLatencyMSP95)
	}
	if rep.DriftPPMEstimate < 100 || rep.DriftPPMEstimate > 140 {
		t.Fatalf("drift estimate %.2f ppm outside [100,140]", rep.DriftPPMEstimate)
	}
	if rep.AudioSamples <= 0 {
		t.Fatal("no audio samples produced")
	}
}

func TestRunDeterministicAcrossRepeats(t *testing.T) {
	r1, _ := Run(testCfg())
	r2, _ := Run(testCfg())
	if r1.EventSHA256 != r2.EventSHA256 {
		t.Fatalf("event hash differs across runs:\n%s\n%s", r1.EventSHA256, r2.EventSHA256)
	}
	if r1.AudioSamples != r2.AudioSamples || r1.RecoveryCount != r2.RecoveryCount {
		t.Fatal("counters differ across runs")
	}
}

func TestRejectsInvalidParameters(t *testing.T) {
	cases := []Config{
		{DurationS: 0, LossBurst: 3, ChunkMS: 20},
		{DurationS: 90000, LossBurst: 3, ChunkMS: 20}, // over 24h
		{DurationS: 10, LossBurst: 4, ChunkMS: 20},    // burst over 3
		{DurationS: 10, LossBurst: 1, ChunkMS: 0},     // bad chunk
	}
	for i, cfg := range cases {
		if _, err := Run(cfg); err == nil {
			t.Fatalf("case %d accepted invalid config %+v", i, cfg)
		}
	}
}

func TestCheckpointResumeMatchesUninterrupted(t *testing.T) {
	cfg := testCfg()
	direct, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// checkpoint at several points, including mid-hole and near stream start
	for _, at := range []int{1, 137, 500, 900} {
		resumed, err := RunWithCheckpoint(cfg, at)
		if err != nil {
			t.Fatalf("resume at %d failed: %v", at, err)
		}
		if resumed.EventSHA256 != direct.EventSHA256 {
			t.Fatalf("at %d: resumed hash %s != direct %s",
				at, resumed.EventSHA256, direct.EventSHA256)
		}
		if resumed.AudioSamples != direct.AudioSamples {
			t.Fatalf("at %d: audio samples discontinuous %d != %d",
				at, resumed.AudioSamples, direct.AudioSamples)
		}
		if resumed.RecoveryCount != direct.RecoveryCount {
			t.Fatalf("at %d: recovery count %d != %d",
				at, resumed.RecoveryCount, direct.RecoveryCount)
		}
		if resumed.Paths["checkpoints"] != 1 {
			t.Fatalf("at %d: checkpoint path not counted", at)
		}
	}
}

func TestThreeAcceptancePathsCounted(t *testing.T) {
	// normal path
	normal, err := Run(testCfg())
	if err != nil {
		t.Fatal(err)
	}
	if normal.Paths["normal_events"] == 0 {
		t.Fatal("normal path produced no events")
	}
	// corrupt-rejection path: bad packets are counted, never crash the run
	if normal.Paths["corrupt_rejected"] == 0 {
		t.Fatal("corrupt packet path not exercised by fixture")
	}
	// checkpoint path
	resumed, err := RunWithCheckpoint(testCfg(), 400)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Paths["checkpoints"] != 1 {
		t.Fatal("checkpoint path not counted")
	}
	t.Logf("paths: normal=%d corrupt=%d duplicates=%d late=%d checkpoints=%d",
		normal.Paths["normal_events"], normal.Paths["corrupt_rejected"],
		normal.Paths["duplicates"], normal.Paths["late_rejected"],
		resumed.Paths["checkpoints"])
}
