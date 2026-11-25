package main

import (
	"encoding/json"
	"fmt"
	"os"

	"mediapipeline/internal/app"
	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
)

func printSummary(s *app.Summary) {
	if s == nil {
		return
	}
	data, _ := json.Marshal(s)
	fmt.Println(string(data))
	// Stable key=value line for acceptance greps.
	fmt.Printf("state=%s representations=%d aligned_segments=%d committed_segments=%d queue_peak=%d manifest_valid=%t\n",
		s.State, s.Representations, s.AlignedSegments, s.CommittedSegments, s.QueuePeak, s.ManifestValid)
	if s.ManifestSHA256 != "" {
		fmt.Printf("manifest_sha256=%s provenance_diff=%d gap_count=%d regression_count=%d\n",
			s.ManifestSHA256, s.ProvenanceDiff, s.GapCount, s.RegressionCount)
	}
	if s.ProvenanceSHA256 != "" {
		fmt.Printf("provenance_sha256=%s\n", s.ProvenanceSHA256)
	}
	if s.CheckpointStage != "" {
		fmt.Printf("checkpoint_stage=%s checkpoint_version=%d\n", s.CheckpointStage, s.CheckpointVersion)
	}
	if s.SafeResumeStage != "" {
		fmt.Printf("safe_resume_stage=%s\n", s.SafeResumeStage)
	}
	if len(s.ConflictDiffs) > 0 {
		for _, d := range s.ConflictDiffs {
			fmt.Printf("conflict_diff segment=%d field=%s previous=%s current=%s\n",
				d.SegmentIndex, d.Field, d.Previous, d.Current)
		}
	}
}

func printVerify(v *app.VerifyResult) {
	data, _ := json.Marshal(v)
	fmt.Println(string(data))
	fmt.Printf("segment_count=%d representation_count=%d gap_count=%d regression_count=%d manifest_sha256=%s valid=%t\n",
		v.SegmentCount, v.RepresentationCount, v.GapCount, v.RegressionCount, v.ManifestSHA256, v.Valid)
}

func printFault(err error) {
	fe, ok := err.(*fault.Error)
	if !ok {
		fmt.Printf("error=internal detail=%q\n", err.Error())
		return
	}
	fmt.Printf("error=%s", fe.Code)
	if fe.Input != "" {
		fmt.Printf(" input=%s", fe.Input)
	}
	if fe.Hint != "" {
		fmt.Printf(" recovery_hint=%s", fe.Hint)
	}
	fmt.Println()
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  mediapipeline demo [--input DIR] [--case CASE] [--representations N]")
	fmt.Fprintln(w, "                       [--queue N] [--interrupt-after STAGE] [--resume] [--repeat N]")
	fmt.Fprintln(w, "  mediapipeline verify --manifest PATH")
}

func validCase(c fixture.Case) bool {
	switch c {
	case fixture.CaseNormal, fixture.CaseTruncatedBox, fixture.CaseTFDTRegression,
		fixture.CaseMfhdGap, fixture.CaseDuplicateConflict, fixture.CaseDuplicateFold,
		fixture.CaseMisaligned:
		return true
	}
	return false
}

func validStage(s checkpoint.Stage) bool {
	switch s {
	case checkpoint.StageScan, checkpoint.StageAlign, checkpoint.StageManifest, checkpoint.StageComplete:
		return true
	}
	return false
}
