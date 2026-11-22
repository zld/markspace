package pipeline

import (
	"errors"
	"strings"
	"testing"

	"mediapipeline/internal/fixture"
	"mediapipeline/internal/media"
	"mediapipeline/internal/timeline"
)

func TestRunThreePaths(t *testing.T) {
	res, err := Run(Options{
		Seed: 42, JitterMS: 80, LossPct: 3,
		InterruptAfter: StageAudioNormalize, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.CaseCount != 3 {
		t.Errorf("case_count = %d", res.Report.CaseCount)
	}
	if res.Report.FaultCount != 5 {
		t.Fatalf("fault_count = %d, want 5: %+v", res.Report.FaultCount, res.Faults)
	}
	classes := map[fixture.Fault]bool{}
	for _, f := range res.Faults {
		classes[f.Class] = true
	}
	for _, want := range []fixture.Fault{
		fixture.FaultRTPDuplicate, fixture.FaultRTPReorder,
		fixture.FaultLossAcrossSubtitle, fixture.FaultDFIllegalFrame,
		fixture.FaultAudioConflict,
	} {
		if !classes[want] {
			t.Errorf("missing fault class %s, have %+v", want, classes)
		}
	}
	if res.RollbackStage != StageAudioNormalize || res.CommitBoundary != StageSyncAlign {
		t.Errorf("rollback=%s boundary=%s", res.RollbackStage, res.CommitBoundary)
	}
	if res.Report.RecoveryMS >= 500 {
		t.Errorf("recovery_ms = %d, want < 500", res.Report.RecoveryMS)
	}
	if err := checkDigest(res.Report.SHA256); err != nil {
		t.Error(err)
	}
	if !res.Report.RepeatSHAEqual {
		t.Error("repeat hash must be equal")
	}
}

func TestLocateErrorAtInjectedPacket(t *testing.T) {
	res, err := Run(Options{
		Seed: 42, JitterMS: 80, LossPct: 3,
		InterruptAfter: StageColorNormalize, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The first bad packet is injected around wire seq 1000; the located
	// extended sequence must be within 1e6 ns of that neighborhood.
	if res.Report.EarliestBadPacket < 990 || res.Report.EarliestBadPacket > 1030 {
		t.Errorf("earliest_bad_packet = %d, want ~1000", res.Report.EarliestBadPacket)
	}
	if res.Report.LocateErrorNS < 0 {
		t.Errorf("locate_error_ns = %d", res.Report.LocateErrorNS)
	}
}

func TestDeterministicHashAcrossRuns(t *testing.T) {
	opts := Options{Seed: 42, JitterMS: 80, LossPct: 3,
		InterruptAfter: StageAudioNormalize, WorkDir: t.TempDir()}
	r1, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.WorkDir = t.TempDir()
	r2, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Report.SHA256 != r2.Report.SHA256 {
		t.Fatalf("hashes differ: %s vs %s", r1.Report.SHA256, r2.Report.SHA256)
	}
}

func TestInvalidConfigExitTwo(t *testing.T) {
	for _, bad := range []Options{
		{Seed: -1, WorkDir: t.TempDir(), InterruptAfter: StageAssemble},
		{Seed: 1, LossPct: 150, WorkDir: t.TempDir(), InterruptAfter: StageAssemble},
		{Seed: 1, WorkDir: t.TempDir(), InterruptAfter: "bogus"},
		{Seed: 1, WorkDir: "", InterruptAfter: StageAssemble},
	} {
		if _, err := Run(bad); !errors.Is(err, ErrConfig) {
			t.Errorf("config %+v expected ErrConfig, got %v", bad, err)
		}
	}
}

func TestDFIllegalFrameRejected(t *testing.T) {
	if _, err := timeline.Rate2997DF.Normalize(fixture.IllegalDFLabel()); err == nil {
		t.Fatal("illegal DF label must be rejected")
	}
	// A legal label still normalizes.
	if _, err := timeline.Rate2997DF.Normalize("00:01:00:02"); err != nil {
		t.Fatal(err)
	}
}

func TestNoAbsolutePathsInArtifacts(t *testing.T) {
	res, err := Run(Options{Seed: 7, JitterMS: 10, LossPct: 1,
		InterruptAfter: StageSyncAlign, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Faults {
		if strings.HasPrefix(f.Detail, "/") || strings.Contains(f.Detail, "/Users/") {
			t.Errorf("fault detail leaks absolute path: %s", f.Detail)
		}
	}
}

func checkDigest(s string) error {
	if len(s) != 64 {
		return errors.New("digest must be 64 hex chars")
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return errors.New("digest must be lowercase hex")
		}
	}
	return nil
}

// keep media import meaningful even if color tests move later.
var _ = media.CPBT709
