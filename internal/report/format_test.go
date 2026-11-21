package report

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"loudcheck/internal/batch"
	"loudcheck/internal/loudness"
	"loudcheck/internal/policy"
)

// sample builds a measured file report with a valid integrated loudness and
// true peak, plus a passing verdict.
func sample() batch.FileReport {
	m := loudness.Result{
		IntegratedLUFS:  -23.2,
		IntegratedValid: true,
		GatedBlockCount: 20,
		TotalBlockCount: 20,
		ShortTermPeakLUFS: -23.1,
		ShortTermValid:    true,
		TruePeak:          math.Pow(10, -3.5/20),
		SamplePeak:        math.Pow(10, -4.0/20),
	}
	return batch.FileReport{
		Path:    "song.wav",
		Layout:  "stereo",
		Format:  "pcm24/2ch/48000Hz",
		Channels: []string{"FL", "FR"},
		Measure: m,
		Verdict: policy.Verdict{Measured: true, IntegratedPass: true, TruePeakPass: true, Pass: true},
	}
}

func TestTextReportContainsAllMeasurements(t *testing.T) {
	s := batch.Summary{Results: []batch.FileReport{sample()}, Passed: 1}
	var buf bytes.Buffer
	if err := WriteBatch(&buf, s, Text); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"file=song.wav", "integrated_lufs=-23.20", "short_term_peak_lufs=-23.10",
		"true_peak_dbtp=-3.50", "sample_peak_dbtp=-4.00",
		"integrated_pass=true", "true_peak_pass=true", "result=PASS",
		"files=1 passed=1 failed_checks=0 errors=0 result=PASS",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text report missing %q\n%s", want, out)
		}
	}
}

func TestTextReportShowsUnmeasurableAsNA(t *testing.T) {
	r := batch.FileReport{
		Path:    "silence.wav",
		Layout:  "mono",
		Format:  "pcm16/1ch/48000Hz",
		Measure: loudness.Result{ShortTermInsufficient: true},
		Verdict: policy.Verdict{Measured: false, Failures: []string{"integrated loudness is not measurable"}},
	}
	s := batch.Summary{Results: []batch.FileReport{r}, NotPassed: 1}
	var buf bytes.Buffer
	if err := WriteBatch(&buf, s, Text); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "integrated_lufs=n/a") {
		t.Errorf("unmeasurable loudness should print n/a:\n%s", out)
	}
	if strings.Contains(out, "integrated_lufs=0.00") || strings.Contains(out, "-inf") {
		t.Errorf("unmeasurable loudness must not become a number:\n%s", out)
	}
	if !strings.Contains(out, "result=FAIL") {
		t.Errorf("unmeasurable file should not pass:\n%s", out)
	}
	if !strings.Contains(out, "insufficient") {
		t.Errorf("short-term insufficiency should be flagged:\n%s", out)
	}
}

func TestJSONReportUsesNullForUnknownValues(t *testing.T) {
	r := batch.FileReport{Path: "silence.wav", Measure: loudness.Result{}, Verdict: policy.Verdict{}}
	var buf bytes.Buffer
	if err := WriteSingle(&buf, r, JSON); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	for _, key := range []string{"integrated_lufs", "short_term_peak_lufs", "true_peak_dbtp", "sample_peak_dbtp"} {
		v, ok := payload[key]
		if !ok {
			t.Errorf("%s missing from JSON", key)
			continue
		}
		if v != nil {
			t.Errorf("%s = %v, want null", key, v)
		}
	}
	for _, key := range []string{"integrated_valid", "short_term_valid", "integrated_pass", "true_peak_pass", "result"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("%s missing from JSON", key)
		}
	}
}

func TestJSONReportValuesAndBatchSummary(t *testing.T) {
	s := batch.Summary{Results: []batch.FileReport{sample()}, Passed: 1}
	var buf bytes.Buffer
	if err := WriteBatch(&buf, s, JSON); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Files   int     `json:"files"`
		Passed  int     `json:"passed"`
		Result  string  `json:"result"`
		Results []struct {
			Integrated *float64 `json:"integrated_lufs"`
			TruePeak   *float64 `json:"true_peak_dbtp"`
			Result     bool     `json:"result"`
		} `json:"results"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload.Files != 1 || payload.Passed != 1 || payload.Result != "PASS" {
		t.Fatalf("summary = %+v", payload)
	}
	if len(payload.Results) != 1 || payload.Results[0].Integrated == nil || payload.Results[0].TruePeak == nil {
		t.Fatalf("missing values: %+v", payload.Results)
	}
	if math.Abs(*payload.Results[0].Integrated+23.2) > 1e-9 {
		t.Errorf("integrated = %v", *payload.Results[0].Integrated)
	}
	if !payload.Results[0].Result {
		t.Error("per-file result should be true")
	}
}

func TestJSONReportsPerFileError(t *testing.T) {
	r := batch.FileReport{Path: "broken.wav", Err: "wav: truncated chunk"}
	var buf bytes.Buffer
	if err := WriteSingle(&buf, r, JSON); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["error"] != "wav: truncated chunk" {
		t.Errorf("error = %v", payload["error"])
	}
	if payload["result"] != false {
		t.Errorf("result = %v, want false", payload["result"])
	}
}

func TestExitCodes(t *testing.T) {
	pass := batch.Summary{Results: []batch.FileReport{{}}, Passed: 1}
	fail := batch.Summary{Results: []batch.FileReport{{}}, NotPassed: 1}
	errs := batch.Summary{Results: []batch.FileReport{{}, {}}, Passed: 1, Failed: 1}
	for _, tc := range []struct {
		name string
		s    batch.Summary
		bad  bool
		want int
	}{
		{"pass", pass, false, 0},
		{"threshold", fail, false, 1},
		{"usage", pass, true, 2},
		{"file error", errs, false, 3},
	} {
		if got := ExitCode(tc.s, tc.bad); got != tc.want {
			t.Errorf("%s: exit = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{"text": Text, "": Text, "JSON": JSON, "json": JSON} {
		got, err := ParseFormat(in)
		if err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("expected an error for an unknown format")
	}
}

func TestBatchResultNames(t *testing.T) {
	cases := []struct {
		s    batch.Summary
		want string
	}{
		{batch.Summary{}, "EMPTY"},
		{batch.Summary{Results: []batch.FileReport{{}}, Passed: 1}, "PASS"},
		{batch.Summary{Results: []batch.FileReport{{}}, NotPassed: 1}, "FAIL"},
		{batch.Summary{Results: []batch.FileReport{{}}, Failed: 1}, "ERROR"},
	}
	for _, tc := range cases {
		if got := batchResult(tc.s); got != tc.want {
			t.Errorf("batchResult(%+v) = %q, want %q", tc.s, got, tc.want)
		}
	}
}
