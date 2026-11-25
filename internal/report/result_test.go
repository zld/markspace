package report

import (
	"strings"
	"testing"

	"packcheck/internal/model"
)

func TestBudgetAndExitCodes(t *testing.T) {
	cases := []struct {
		name      string
		input     int64
		output    int64
		allowance int64
		runtime   bool
		wantCode  int
		wantPass  bool
	}{
		{"exact budget", 1000, 1000, 0, false, ExitOK, true},
		{"over budget", 1000, 900, 0, false, ExitThreshold, false},
		{"within allowance", 1000, 995, 5, false, ExitOK, true},
		{"negative delta over allowance", 1000, 1200, 100, false, ExitThreshold, false},
		{"runtime wins over threshold", 1000, 1, 0, true, ExitRuntime, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &Result{
				InputBytes: c.input, OutputBytes: c.output, RuntimeError: c.runtime,
				BudgetDeltaBytes: BudgetDelta(c.input, c.output),
			}
			r.EvaluateBudget(c.allowance)
			if r.ThresholdPass != c.wantPass {
				t.Errorf("threshold=%v want %v", r.ThresholdPass, c.wantPass)
			}
			if r.ExitCode() != c.wantCode {
				t.Errorf("exit=%d want %d", r.ExitCode(), c.wantCode)
			}
		})
	}
}

func TestRenderContainsShellFields(t *testing.T) {
	r := &Result{
		LayerCount: 3, AlignedBoundaries: true, InputBytes: 500, OutputBytes: 480,
		BudgetDeltaBytes: 20, BudgetThreshold: 100, ThresholdPass: true,
		PeakQueueDepth: 2, ManifestDigest: "abc123",
		Conclusions: []LayerConclusion{{
			Representation: "low", ContainerOK: true, SubtitleOK: true,
			BoundaryOK: true, Verdict: "passed", InManifest: true,
		}},
		Anomalies: []AnomalyInterval{{
			Representation: "mid", Code: model.CodeBoundaryDrift,
			ByteOffset: 338, StartTS: 90000, EndTS: 93600,
		}},
		Issues: []model.Issue{{
			Code: model.CodeSlowConsumer, Severity: model.SeverityError,
			Layer: model.LayerStream, Representation: "mid", ByteOffset: 40,
			Message: "slow consumer timed out; layer isolated",
		}},
		ManifestText: "entry low bytes=160 boundaries=4 verdict=passed\n",
	}
	out := r.Render()
	for _, want := range []string{
		"layer_count=3", "aligned_boundaries=true", "budget_delta_bytes=20",
		"threshold=pass", "peak_queue_depth=2", "manifest_digest=abc123",
		"layer name=low container=pass", "anomaly representation=mid code=boundary-drift",
		"byte_offset=338", "rtp_start=90000", "issue error/slow-consumer",
		"entry low bytes=160", "exit=0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q:\n%s", want, out)
		}
	}
}
