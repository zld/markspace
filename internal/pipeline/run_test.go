package pipeline

import (
	"context"
	"os"
	"runtime"
	"sync"
	"testing"

	"mediascan/internal/fixture"
)

var (
	sharedOnce sync.Once
	sharedGT   *fixture.GroundTruth
	sharedErr  error
	sharedDir  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedDir != "" {
		_ = os.RemoveAll(sharedDir)
	}
	os.Exit(code)
}

func groundTruth(t *testing.T) *fixture.GroundTruth {
	t.Helper()
	sharedOnce.Do(func() {
		sharedDir, sharedErr = os.MkdirTemp("", "mediascan-shared-*")
		if sharedErr != nil {
			return
		}
		sharedGT, sharedErr = fixture.Generate(sharedDir)
	})
	if sharedErr != nil {
		t.Fatal(sharedErr)
	}
	return sharedGT
}

func runScan(t *testing.T, input, cp string, resume bool, hook int) *Result {
	t.Helper()
	res, err := Run(context.Background(), Config{
		Input: input, CheckpointPath: cp, Resume: resume,
		MemoryMiB: 8, InterruptAfterSegments: hook,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestGoodScanCleanDeterministicBounded(t *testing.T) {
	gt := groundTruth(t)
	runtime.GC()
	r1 := runScan(t, gt.GoodManifest, "", false, 0)
	if r1.ExitCode != 0 {
		t.Fatalf("exit=%d faults=%s", r1.ExitCode, FaultTable(r1))
	}
	if r1.Report.SHA256 == "" {
		t.Fatal("empty sha")
	}
	if r1.Report.GOPMinTicks != 1_600_000 || r1.Report.GOPMaxTicks != 1_600_000 {
		t.Fatalf("gop min/max=%d/%d", r1.Report.GOPMinTicks, r1.Report.GOPMaxTicks)
	}
	if r1.MemoryPeakBytes <= 0 || r1.MemoryPeakBytes > 8<<20 {
		t.Fatalf("memory peak=%d outside (0,8MiB]", r1.MemoryPeakBytes)
	}
	r2 := runScan(t, gt.GoodManifest, "", false, 0)
	if r1.Report.SHA256 != r2.Report.SHA256 {
		t.Fatalf("sha not deterministic: %s vs %s", r1.Report.SHA256, r2.Report.SHA256)
	}
}

func TestCorruptScanLocatesPlantedFault(t *testing.T) {
	gt := groundTruth(t)
	cp := t.TempDir() + "/cp"
	r := runScan(t, gt.CorruptManifest, cp, false, 0)
	if r.ExitCode != 2 {
		t.Fatalf("exit=%d want 2", r.ExitCode)
	}
	if r.PrimaryOffset != gt.Corrupt.CutOffsetBytes {
		t.Fatalf("offset=%d want cut %d", r.PrimaryOffset, gt.Corrupt.CutOffsetBytes)
	}
	if r.PrimaryTime != gt.Corrupt.SampleTimeUnified {
		t.Fatalf("time=%d want %d", r.PrimaryTime, gt.Corrupt.SampleTimeUnified)
	}
	if len(r.Report.Faults) == 0 || r.Report.Faults[0].Code != "truncated_box" {
		t.Fatalf("faults=%+v", r.Report.Faults)
	}
	// A second cold run (checkpoint reset) yields the identical checksum.
	cp2 := t.TempDir() + "/cp2"
	r2 := runScan(t, gt.CorruptManifest, cp2, false, 0)
	if r.Report.SHA256 != r2.Report.SHA256 {
		t.Fatalf("cold checksums differ:\n%s\n%s", r.Report.SHA256, r2.Report.SHA256)
	}
}

func TestSpecialScenarios(t *testing.T) {
	gt := groundTruth(t)

	wd := runScan(t, gt.WrapDriftManifest, t.TempDir()+"/wd", false, 0)
	if wd.WrapEvents != 1 || wd.DriftPPM < 495 || wd.DriftPPM > 505 {
		t.Fatalf("wrap=%d drift=%d want 1/~500", wd.WrapEvents, wd.DriftPPM)
	}

	vfr := runScan(t, gt.VFRManifest, t.TempDir()+"/vfr", false, 0)
	if vfr.ExitCode != 0 || vfr.Report.GOPMinTicks == vfr.Report.GOPMaxTicks {
		t.Fatalf("vfr min=%d max=%d", vfr.Report.GOPMinTicks, vfr.Report.GOPMaxTicks)
	}

	disc := runScan(t, gt.DiscontinuityManifest, t.TempDir()+"/disc", false, 0)
	if disc.ExitCode != 0 || disc.Report.PeriodCount != 2 {
		t.Fatalf("discontinuity exit=%d periods=%d", disc.ExitCode, disc.Report.PeriodCount)
	}

	bad := runScan(t, gt.DashMisalignedManifest, t.TempDir()+"/dash", false, 0)
	if bad.ExitCode != 2 || bad.Report.ErrorCount == 0 {
		t.Fatalf("misaligned dash should report errors, exit=%d", bad.ExitCode)
	}
}
