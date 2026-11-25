package pipeline

import (
	"testing"
	"time"
)

func TestRunNormalPipeline(t *testing.T) {
	out, err := Run(Options{
		Seed: 17, Duration: 120 * time.Second, SegmentDuration: 6 * time.Second,
		Bitrates: []int{800000, 1600000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 0 || out.Report == nil {
		t.Fatalf("outcome=%+v", out)
	}
	if out.Report.TotalByteError != 0 || out.Report.MaxTimeErrorTicks != 0 {
		t.Fatalf("errors time=%d byte=%d", out.Report.MaxTimeErrorTicks, out.Report.TotalByteError)
	}
	if out.Report.LossCount == 0 || out.Report.DuplicateCount == 0 {
		t.Fatalf("rtp counts loss=%d dup=%d", out.Report.LossCount, out.Report.DuplicateCount)
	}
}

func TestRunTruncatedPipelineLocationAndExit(t *testing.T) {
	out, err := Run(Options{
		Seed: 17, Duration: 120 * time.Second, SegmentDuration: 6 * time.Second,
		Bitrates: []int{800000}, Fault: "truncated-stbl",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 2 {
		t.Fatalf("exit=%d", out.ExitCode)
	}
	if out.Report.FailureTrack != 1 || out.Report.FailureSample != 37 {
		t.Fatalf("failure=%d/%d", out.Report.FailureTrack, out.Report.FailureSample)
	}
	if out.Report.RecoveryState != "recovery_required" {
		t.Fatalf("state=%s", out.Report.RecoveryState)
	}
}

func TestRunEditRecoveryPipeline(t *testing.T) {
	out, err := Run(Options{
		Seed: 17, Duration: 120 * time.Second, SegmentDuration: 6 * time.Second,
		Bitrates: []int{800000}, Fault: "edit-out-of-range", Recover: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 2 || out.Report.RecoveryState != "recovered-edit-clamped" {
		t.Fatalf("exit=%d state=%s", out.ExitCode, out.Report.RecoveryState)
	}
	if len(out.Report.EditReports) != 1 || len(out.Report.EditReports[0].Adjustments) != 1 {
		t.Fatalf("edit reports=%+v", out.Report.EditReports)
	}
}
