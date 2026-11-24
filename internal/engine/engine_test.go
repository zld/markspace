package engine

import (
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/fixture"
	"mediasync/internal/model"
)

func smallCapture(t *testing.T, seed int64) *Input {
	t.Helper()
	return captureWith(t, seed, 4000, 2)
}

func captureWith(t *testing.T, seed, durationMS int64, cameras int) *Input {
	t.Helper()
	dir := t.TempDir()
	cfg := fixture.DefaultCaptureConfig(seed)
	cfg.DurationMS = durationMS
	cfg.Cameras = cameras
	mp, ep, err := fixture.Generate(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	in, err := LoadInput(mp, ep)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func testOptions(t *testing.T, seed int64) Options {
	t.Helper()
	return Options{WorkDir: filepath.Join(t.TempDir(), "ckpt"), Seed: seed, WrapBits: 16, DriftPPM: 1000, LateWindowMS: 500}
}

func TestRunDeterministic(t *testing.T) {
	in := smallCapture(t, 17)
	var digs []string
	for i := 0; i < 2; i++ {
		eng, err := New(testOptions(t, 17), in.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		res, err := eng.Run(in)
		if err != nil {
			t.Fatal(err)
		}
		digs = append(digs, res.Digest)
	}
	if digs[0] != digs[1] {
		t.Fatalf("digest changed between identical runs: %s vs %s", digs[0], digs[1])
	}
}

func TestRunOrderedOutput(t *testing.T) {
	in := smallCapture(t, 18)
	eng, _ := New(testOptions(t, 18), in.Manifest)
	res, err := eng.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(res.Records); i++ {
		a, b := res.Records[i-1], res.Records[i]
		if a.Corrected > b.Corrected {
			t.Fatalf("record %d is earlier than %d", i, i-1)
		}
		if a.Corrected == b.Corrected && a.TrackID > b.TrackID {
			t.Fatalf("equal timestamps not ordered by track at %d", i)
		}
	}
}

func TestRunCountsDuplicatesOnce(t *testing.T) {
	in := smallCapture(t, 19)
	eng, _ := New(testOptions(t, 19), in.Manifest)
	res, err := eng.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.PacketsDup == 0 {
		t.Fatal("fixture should contain duplicate deliveries")
	}
	repaired := int64(0)
	for _, ev := range in.Events {
		if ev.IsRepair() {
			repaired++
		}
	}
	if res.Summary.PacketTotal != int64(len(in.Events))-repaired {
		t.Errorf("packet total = %d, media events = %d", res.Summary.PacketTotal, int64(len(in.Events))-repaired)
	}
	// Every accepted record has a distinct (track, sequence) pair.
	seen := map[[2]interface{}]bool{}
	for _, r := range res.Records {
		k := [2]interface{}{r.TrackID, r.ExtSeq}
		if seen[k] {
			t.Fatalf("duplicate delivered record for track %d seq %d", r.TrackID, r.ExtSeq)
		}
		seen[k] = true
	}
}

func TestRunDetectsLossAndRecovery(t *testing.T) {
	in := smallCapture(t, 20)
	eng, _ := New(testOptions(t, 20), in.Manifest)
	res, err := eng.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Lost == 0 {
		t.Error("fixture contains loss bursts but none were classified")
	}
	if res.Summary.Recovered == 0 {
		t.Error("fixture contains redundancy records but none recovered")
	}
	if res.Summary.Recovered > res.Summary.Lost {
		t.Errorf("recovered %d exceeds lost %d", res.Summary.Recovered, res.Summary.Lost)
	}
}

func TestRunMeasuresSkew(t *testing.T) {
	in := smallCapture(t, 17)
	eng, _ := New(testOptions(t, 17), in.Manifest)
	res, err := eng.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.MaxAVSkewMS > 20 {
		t.Errorf("max A/V skew = %.3f ms exceeds 20 ms", res.Summary.MaxAVSkewMS)
	}
}

func TestRunDetectsLateEvents(t *testing.T) {
	in := smallCapture(t, 17)
	eng, _ := New(testOptions(t, 17), in.Manifest)
	res, err := eng.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.LateRecomputed == 0 {
		t.Fatal("fixture injects late deliveries but none were detected")
	}
	if len(res.LateEvents) != int(res.Summary.LateEvents) {
		t.Errorf("late event list and counter disagree")
	}
	for _, l := range res.LateEvents {
		if l.AffectedAt > l.Corrected {
			t.Errorf("affected time %d is after the late event %d", l.AffectedAt, l.Corrected)
		}
		if l.RewindIndex < 0 {
			t.Errorf("late event has no rewind position")
		}
	}
}

func TestResumeMatchesUninterrupted(t *testing.T) {
	in := captureWith(t, 17, 20_000, 3)
	opts := testOptions(t, 17)
	opts.CheckpointEvery = 500
	ref, err := New(opts, in.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	refRes, err := ref.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	_ = refRes

	opts.InterruptAfter = 1200
	interrupted, err := New(opts, in.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	first, err := interrupted.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if first.CheckpointCount == 0 {
		t.Fatal("interrupted run wrote no checkpoints")
	}

	// The reference is a run with interruption disabled but the same
	// checkpoint interval: the resumed run must reproduce its full digest.
	resumed, err := New(opts, in.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resumed.ResumeFromCheckpoint(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResumedFrom != 1000 {
		t.Errorf("resumed from event %d, want the 1000-event checkpoint", got.ResumedFrom)
	}
	fullOpts := opts
	fullOpts.InterruptAfter = 0
	full, err := New(fullOpts, in.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	fullRes, err := full.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != fullRes.Digest {
		t.Errorf("resumed digest %s != uninterrupted %s", got.Digest, fullRes.Digest)
	}
	if got.Summary.ResumeMS < 0 {
		t.Errorf("resume latency = %v", got.Summary.ResumeMS)
	}
}

func TestResumeFallsBackOnCorruption(t *testing.T) {
	in := captureWith(t, 18, 20_000, 3)
	opts := testOptions(t, 18)
	opts.CheckpointEvery = 500
	opts.InterruptAfter = 2000
	interrupted, _ := New(opts, in.Manifest)
	if _, err := interrupted.Run(in); err != nil {
		t.Fatal(err)
	}
	// Append a damaged record to the checkpoint log.
	logPath := filepath.Join(opts.WorkDir, "checkpoints.jsonl")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"version":1,"index":9999,"digest":"deadbeef"}` + "\n")
	f.Close()

	resumed, _ := New(opts, in.Manifest)
	got, err := resumed.ResumeFromCheckpoint(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.FallbackReason == "" {
		t.Error("corrupt log should report a fallback reason")
	}
	// Checkpoints are written every 500 events, so the last clean one before
	// the 2000-event interrupt is 1500.
	if got.ResumedFrom != 1500 {
		t.Errorf("resumed from %d, want the last valid checkpoint at 1500", got.ResumedFrom)
	}
}

func TestRunWithoutManifestFails(t *testing.T) {
	eng, err := New(Options{WorkDir: t.TempDir()}, nil)
	if err == nil {
		t.Fatalf("expected a nil-manifest error, got engine %v", eng)
	}
}

func TestOptionsValidation(t *testing.T) {
	bad := []Options{
		{},
		{WorkDir: "/tmp", WrapBits: 64},
		{WorkDir: "/tmp", WrapBits: 16, TsWrapBits: 64},
		{WorkDir: "/tmp", WrapBits: 16, TsWrapBits: 32, DriftPPM: -1},
		{WorkDir: "/tmp", WrapBits: 16, TsWrapBits: 32, DriftPPM: 200, LateWindowMS: -1},
		{WorkDir: "/tmp", WrapBits: 16, TsWrapBits: 32, DriftPPM: 200, ReorderPackets: -1},
	}
	for i, o := range bad {
		if err := o.Validate(); err == nil {
			t.Errorf("option set %d should be rejected: %+v", i, o)
		}
	}
}

func TestEventUnknownTrackReported(t *testing.T) {
	in := smallCapture(t, 17)
	in.Events = append(in.Events, model.PacketEvent{TrackID: 99, Kind: model.KindVideo, Seq: 1, Arrival: 0})
	in.EventBytes = append(in.EventBytes, 0)
	eng, _ := New(testOptions(t, 17), in.Manifest)
	res, err := eng.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("unknown track should produce a diagnostic")
	}
	if res.Errors[0].Loc.TrackID != 99 {
		t.Errorf("diagnostic track = %d, want 99", res.Errors[0].Loc.TrackID)
	}
}
