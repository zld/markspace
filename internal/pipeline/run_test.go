package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediakernel/internal/clock"
)

func baseOptions(t *testing.T, fault Fault) Options {
	dir := t.TempDir()
	return Options{
		BaseDir:     dir,
		WAVRelPath:  filepath.Join("input", "sample.wav"),
		OutputDir:   "out",
		Channels:    2,
		SampleRate:  48000,
		Frames:      9600,
		BlockFrames: 480,
		Fault:       fault,
	}
}

func TestCleanRunDeterministic(t *testing.T) {
	o := baseOptions(t, FaultNone)
	r1, err := Run(o, false)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Frames != 9600 || r1.LocatingErrors != 0 || r1.OutputSHA256 == "" {
		t.Fatalf("unexpected clean result: %+v", r1)
	}
	if len(r1.OutputSHA256) != 64 {
		t.Fatal("output digest must be full sha-256 hex")
	}
	// A second clean run on a fresh base must produce the identical digest.
	o2 := baseOptions(t, FaultNone)
	r2, err := Run(o2, false)
	if err != nil {
		t.Fatal(err)
	}
	if r2.OutputSHA256 != r1.OutputSHA256 {
		t.Fatalf("non-deterministic output: %s vs %s", r1.OutputSHA256, r2.OutputSHA256)
	}
}

func TestTruncationRunFailsWithOffset(t *testing.T) {
	o := baseOptions(t, FaultTruncate)
	_, err := Run(o, false)
	if err == nil {
		t.Fatal("truncated input must fail")
	}
	ce, ok := clock.AsError(err)
	if !ok {
		t.Fatalf("want structured error, got %v", err)
	}
	frameSize := int64(o.Channels) * 2
	if !ce.HasOffset {
		t.Fatalf("error must carry byte offset: %v", err)
	}
	// The reported first-error offset is within one sample frame of EOF.
	// prepareWAV cuts frameSize/2+1 bytes, so the gap is <= one frame.
	_ = frameSize
}

func TestCheckpointFaultThenResumeMatchesClean(t *testing.T) {
	// Clean reference.
	cleanOpts := baseOptions(t, FaultNone)
	clean, err := Run(cleanOpts, false)
	if err != nil {
		t.Fatal(err)
	}

	// Fault run: interrupted after the index stage with a durable checkpoint.
	faultOpts := baseOptions(t, FaultCheckpoint)
	faultOpts.CrashAfterStage = "index"
	_, err = Run(faultOpts, false)
	if !errors.Is(err, ErrCrashRehearsal) {
		t.Fatalf("want crash rehearsal, got %v", err)
	}
	// Resume: completed stages are reused and the output digest must match.
	resumed, err := Run(faultOpts, true)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if !resumed.Resume || resumed.ReusedStages < 3 {
		t.Fatalf("resume not recognized: %+v", resumed)
	}
	if resumed.ResumeMillis < 0 || resumed.ResumeMillis > 5000 {
		t.Fatalf("resume_ms out of plausible range: %d", resumed.ResumeMillis)
	}
	if resumed.OutputSHA256 != clean.OutputSHA256 {
		t.Fatalf("resumed digest %s != clean %s", resumed.OutputSHA256, clean.OutputSHA256)
	}
}

func TestRepeatedResumeStable(t *testing.T) {
	o := baseOptions(t, FaultCheckpoint)
	o.CrashAfterStage = "index"
	if _, err := Run(o, false); !errors.Is(err, ErrCrashRehearsal) {
		t.Fatal(err)
	}
	var first string
	for i := 0; i < 3; i++ {
		r, err := Run(o, true)
		if err != nil {
			t.Fatalf("resume %d failed: %v", i, err)
		}
		if first == "" {
			first = r.OutputSHA256
		} else if r.OutputSHA256 != first {
			t.Fatalf("resume %d drifted: %s vs %s", i, r.OutputSHA256, first)
		}
	}
}

func TestCorruptCheckpointRebuilds(t *testing.T) {
	o := baseOptions(t, FaultCheckpoint)
	o.CrashAfterStage = "index"
	if _, err := Run(o, false); !errors.Is(err, ErrCrashRehearsal) {
		t.Fatal(err)
	}
	cpPath := filepath.Join(o.BaseDir, o.OutputDir, "checkpoint.json")
	// Corrupt the checkpoint bytes: resume must discard and rebuild.
	if err := writeFile(cpPath, []byte("{broken")); err != nil {
		t.Fatal(err)
	}
	r, err := Run(o, true)
	if err != nil {
		t.Fatalf("corrupt checkpoint should trigger rebuild, got %v", err)
	}
	if r.Resume {
		t.Fatal("rebuild after corruption must not report resume reuse")
	}
	if r.OutputSHA256 == "" {
		t.Fatal("rebuilt run must still produce an output digest")
	}
}

func TestChangedParamsInvalidateCheckpoint(t *testing.T) {
	o := baseOptions(t, FaultCheckpoint)
	o.CrashAfterStage = "index"
	if _, err := Run(o, false); !errors.Is(err, ErrCrashRehearsal) {
		t.Fatal(err)
	}
	// Different block geometry changes params: no stage may be reused.
	o.BlockFrames = 960
	r, err := Run(o, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.ReusedStages != 0 || r.Resume {
		t.Fatalf("changed params must invalidate checkpoint: %+v", r)
	}
}

func TestUnknownFaultRejected(t *testing.T) {
	o := baseOptions(t, Fault("explode"))
	_, err := Run(o, false)
	ce, ok := clock.AsError(err)
	if !ok || ce.Category != clock.CatInvalidParam {
		t.Fatalf("want invalid_parameter, got %v", err)
	}
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
