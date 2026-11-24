package pipeline_test

import (
	"testing"

	"mediarc/internal/fixture"
	"mediarc/internal/model"
	"mediarc/internal/pipeline"
	"mediarc/internal/verify"
)

func TestDemoEndToEndThresholdsAndDeterminism(t *testing.T) {
	opt := fixture.Options{Seed: 7, LossPackets: 8, LateMS: 120}
	a, err := pipeline.RunDemo(opt)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pipeline.RunDemo(opt)
	if err != nil {
		t.Fatal(err)
	}
	if a.Remux.SHA256 != b.Remux.SHA256 {
		t.Fatal("repeated runs produced different hashes")
	}
	if a.Timeline.OrderHash != b.Timeline.OrderHash {
		t.Fatal("timeline order hash is nondeterministic")
	}
	if !verify.AcceptanceOK(a.Report) {
		t.Fatalf("acceptance thresholds failed: %+v", a.Report)
	}
	if a.Report.InputSampleCount != 78 || a.Report.OutputSampleCount != 78 {
		t.Fatalf("sample counts = %d/%d", a.Report.InputSampleCount, a.Report.OutputSampleCount)
	}
	if !a.Report.SampleHashesMatch || !a.Report.PropertiesMatch {
		t.Fatal("verification did not match")
	}
	if a.Report.RewindCount == 0 {
		t.Fatal("late event did not rewind timeline")
	}
}

func TestVerificationCases(t *testing.T) {
	tests := []struct {
		name  string
		code  model.ErrorCode
		check func(*testing.T, *pipeline.CaseResult)
	}{
		{"truncated", model.CodeTruncated, func(t *testing.T, r *pipeline.CaseResult) {
			if r.Offset == nil || *r.Offset <= 0 {
				t.Fatal("missing offset")
			}
		}},
		{"late-boundary", model.CodeLateBoundary, func(t *testing.T, r *pipeline.CaseResult) {
			if r.RewindCount < 2 {
				t.Fatalf("rewind count = %d, want >=2", r.RewindCount)
			}
		}},
		{"color-conflict", model.CodeColorConflict, func(t *testing.T, r *pipeline.CaseResult) {
			if len(r.ConflictFields) == 0 {
				t.Fatal("missing conflict fields")
			}
		}},
		{"loss-over-window", model.CodeLossOverWindow, func(t *testing.T, r *pipeline.CaseResult) {
			if r.Unrecoverable == 0 {
				t.Fatal("missing unrecoverable count")
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := pipeline.RunCase(tc.name)
			if err == nil {
				t.Fatal("expected case error")
			}
			if !model.HasCode(err, tc.code) {
				t.Fatalf("code = %v, want %s (%v)", err, tc.code, err)
			}
			if res == nil {
				t.Fatal("nil case result")
			}
			tc.check(t, res)
		})
	}
}
