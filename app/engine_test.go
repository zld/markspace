package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"medpipe/fixture"
	"medpipe/manifest"
	"medpipe/stream"
	"medpipe/timeline"
)

func TestDemoHealthySummaryFields(t *testing.T) {
	dir := t.TempDir()
	opt := Options{Seed: 17, Rate: 48000, Channels: 6, Queue: 4, SlowMS: 1, OutDir: dir}
	sum, err := Run(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if sum.ExitCode != 0 {
		t.Fatalf("healthy run exit = %d", sum.ExitCode)
	}
	if len(sum.Manifest.Tracks) != 2 {
		t.Fatalf("want 2 track records, got %d", len(sum.Manifest.Tracks))
	}
	for _, tr := range sum.Manifest.Tracks {
		if tr.BitrateDeltaPct > 2 || tr.BitrateDeltaPct < -2 {
			t.Errorf("%s: bitrate delta %.3f outside +/-2%%", tr.TrackID, tr.BitrateDeltaPct)
		}
		if tr.TruePeakDBTP > -1.0 {
			t.Errorf("%s: true peak %.2f above -1 dBTP", tr.TrackID, tr.TruePeakDBTP)
		}
		if tr.AlignmentMillis > 1 || tr.AlignmentMillis < -1 {
			t.Errorf("%s: alignment %d ms outside +/-1", tr.TrackID, tr.AlignmentMillis)
		}
		if tr.OutputFrames != tr.ResampledFrames {
			t.Errorf("%s: frame invariant broken", tr.TrackID)
		}
	}
	if sum.Manifest.PeakQueue > 4 {
		t.Errorf("peak queue %d exceeded capacity 4", sum.Manifest.PeakQueue)
	}
	if sum.Manifest.OutputSHA == "" {
		t.Error("output sha256 missing")
	}
	// Artifacts exist and the MP4 carrier parses back identically.
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("manifest.json missing: %v", err)
	}
	f, err := os.Open(filepath.Join(dir, "manifest.mp4"))
	if err != nil {
		t.Fatalf("manifest.mp4 missing: %v", err)
	}
	defer f.Close()
	parsed, err := manifest.ReadMP4(f)
	if err != nil {
		t.Fatalf("mp4 carrier unreadable: %v", err)
	}
	if parsed.OutputSHA != sum.Manifest.OutputSHA {
		t.Fatal("mp4 round trip changed the manifest hash")
	}
}

func TestHashIdenticalAcrossRepeatedRuns(t *testing.T) {
	run := func(dir string) string {
		opt := Options{Seed: 17, Rate: 48000, Channels: 2, Queue: 4, OutDir: dir}
		sum, err := Run(context.Background(), opt)
		if err != nil {
			t.Fatal(err)
		}
		return sum.Manifest.OutputSHA
	}
	h1 := run(t.TempDir())
	h2 := run(t.TempDir())
	if h1 != h2 {
		t.Fatalf("identical parameters produced different hashes: %s vs %s", h1, h2)
	}
}

func TestFaultFixturesExitTwoAndLocate(t *testing.T) {
	cases := map[string]string{
		fixture.FaultTruncated:  timeline.StageWAV,
		fixture.FaultMissing:    timeline.StageTimeline,
		fixture.FaultMisaligned: timeline.StageTimeline,
		fixture.FaultSeqJump:    timeline.StageRTP,
		fixture.FaultBadLength:  timeline.StageWAV,
	}
	for fault, wantStage := range cases {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			opt := Options{Seed: 17, Rate: 48000, Channels: 2, Queue: 4,
				OutDir: dir, Fixture: fault}
			sum, err := Run(context.Background(), opt)
			if err != nil {
				t.Fatal(err)
			}
			if sum.ExitCode != 2 {
				t.Fatalf("%s: exit code = %d, want 2", fault, sum.ExitCode)
			}
			if sum.ErrorStage != wantStage {
				t.Fatalf("%s: stage = %q, want %q", fault, sum.ErrorStage, wantStage)
			}
			if sum.TrackID == "" {
				t.Fatalf("%s: failure must locate a track", fault)
			}
			if len(sum.Manifest.Failures) != 1 {
				t.Fatalf("%s: manifest must carry exactly one failure", fault)
			}
		})
	}
}

func TestCheckpointSurvivesFailureAndResume(t *testing.T) {
	// First run with the truncated fixture: blocks before the fault commit.
	bad := t.TempDir()
	sum, err := Run(context.Background(), Options{
		Seed: 17, Rate: 48000, Channels: 2, Queue: 4,
		OutDir: bad, Fixture: fixture.FaultTruncated,
	})
	if err != nil || sum.ExitCode != 2 {
		t.Fatalf("fault run: sum=%+v err=%v", sum, err)
	}
	cp := sum.Checkpoint
	if cp.BlockIndex != 2 || cp.Position == 0 {
		t.Fatalf("unexpected checkpoint before fault: %+v", cp)
	}
	// The failure is propagated into the manifest stage as a failure record.
	if len(sum.Manifest.Failures) != 1 || sum.Manifest.Failures[0].Checkpoint.Position != cp.Position {
		t.Fatalf("failure record must carry the committed checkpoint: %+v", sum.Manifest.Failures)
	}
	// Repaired run with the same seed but no fault: processing completes and
	// covers strictly more frames than the failed run had checkpoints for.
	good := t.TempDir()
	fixed, err := Run(context.Background(), Options{
		Seed: 17, Rate: 48000, Channels: 2, Queue: 4, OutDir: good,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fixed.ExitCode != 0 {
		t.Fatal("repaired fixture must complete successfully")
	}
	var recoveredFrames int64
	for _, tr := range fixed.Manifest.Tracks {
		recoveredFrames += tr.OutputFrames
	}
	if recoveredFrames <= cp.Position {
		t.Fatalf("repaired run %d frames did not advance past checkpoint %d",
			recoveredFrames, cp.Position)
	}
	// The monotonic checkpoint store rejects any rollback during resume.
	store := stream.NewMemCheckpoint()
	if err := store.Commit(cp); err != nil {
		t.Fatal(err)
	}
	regressed := cp
	regressed.Position--
	if err := store.Commit(regressed); err == nil {
		t.Fatal("checkpoint regression after resume must be rejected")
	}
}

func TestDurationMismatchPropagatesToManifest(t *testing.T) {
	// A hand-built record whose duration disagrees with the output clock must
	// be rejected at the manifest stage rather than silently written.
	clock, _ := timeline.NewClock(48000)
	b := manifest.NewBuilder(clock, 1, 48000, 2, 4)
	rec := manifest.TrackRecord{
		TrackID: "x", OutputFrames: 48000, ResampledFrames: 48000,
		StartSample: 0, EndSample: 48000,
		DurationNanos: 999_000_000, // one second of frames, wrong duration
		TruePeakDBTP: -2, TargetBitrateBPS: 1, ActualBitrateBPS: 1,
	}
	err := b.AddTrack(rec)
	pe, ok := timeline.AsError(err)
	if !ok || pe.Code != timeline.ErrCodeDurationMismatch || pe.Stage != timeline.StageManifest {
		t.Fatalf("duration mismatch must surface at manifest stage: %v", err)
	}
}

func TestQueueCapacityHardLimitViaCLI(t *testing.T) {
	if _, err := stream.NewBoundedQueue(65); err == nil {
		t.Fatal("queue above the 64-block hard limit must be rejected")
	}
	if _, err := stream.NewBoundedQueue(64); err != nil {
		t.Fatalf("capacity 64 is the legal ceiling: %v", err)
	}
}
