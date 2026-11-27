package report

import (
	"bytes"
	"encoding/json"
	"testing"

	"rtx/internal/clock"
	"rtx/internal/model"
	"rtx/internal/pipeline"
	"rtx/internal/quality"
	"rtx/internal/rtp"
	"rtx/internal/transform"
	"rtx/internal/wav"
)

func stubDrift() *clock.Result      { return &clock.Result{} }
func stubQuality() *quality.Metrics { return &quality.Metrics{} }

func stubInput() *pipeline.Input {
	return &pipeline.Input{
		WAV:       &wav.ParseResult{Header: &wav.Header{}},
		RTP:       &rtp.ParseResult{},
		Unwrapped: &rtp.UnwrapResult{},
	}
}

func isolatedStub() *rtp.UnwrapResult {
	return &rtp.UnwrapResult{Isolated: []model.Range{{Start: 3, End: 5}}}
}

func TestBuildExitCodes(t *testing.T) {
	mk := func(feasible bool) *Summary {
		a := &pipeline.Analysis{
			Drift:   stubDrift(),
			Quality: stubQuality(),
			Config:  &transform.Config{Feasible: feasible},
		}
		return Build(stubInput(), a, &pipeline.Validation{})
	}
	if s := mk(true); s.ExitCode != ExitOK {
		t.Fatalf("feasible config exit %d", s.ExitCode)
	}
	if s := mk(false); s.ExitCode != ExitInfeasible {
		t.Fatalf("infeasible config exit %d", s.ExitCode)
	}
}

func TestWriteValidJSON(t *testing.T) {
	a := &pipeline.Analysis{
		Drift:   stubDrift(),
		Quality: stubQuality(),
		Config:  &transform.Config{Feasible: true},
	}
	s := Build(stubInput(), a, &pipeline.Validation{})
	var buf bytes.Buffer
	if err := Write(&buf, s); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("summary is not valid JSON: %v", err)
	}
	for _, key := range []string{"packet_total", "duplicate_count",
		"reordered_count", "drift_ppm", "quality", "bitrate_bps",
		"budget_ms", "budget_ms_actual", "anomaly_ranges", "items"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing JSON field %q", key)
		}
	}
}

func TestAnomalyRangesCollected(t *testing.T) {
	in := stubInput()
	in.Unwrapped = isolatedStub()
	a := &pipeline.Analysis{
		Drift:   stubDrift(),
		Quality: stubQuality(),
		Config:  &transform.Config{Feasible: true},
	}
	s := Build(in, a, &pipeline.Validation{})
	if len(s.AnomalyRanges) == 0 {
		t.Fatal("isolated ranges must appear in anomaly_ranges")
	}
}
