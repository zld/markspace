package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func fp(v float64) *float64 { return &v }

func TestEvaluate(t *testing.T) {
	gates := Gates{IntegratedMin: fp(-24), IntegratedMax: fp(-16), TruePeakMax: fp(-1)}
	ok := FileReport{IntegratedLUFS: fp(-20), TruePeakDBTP: fp(-6)}
	ok.Evaluate(gates)
	if ok.OverallPass == nil || !*ok.OverallPass || !*ok.LoudnessPass || !*ok.TruePeakPass {
		t.Errorf("in-range measurement must pass: %+v", ok)
	}
	hot := FileReport{IntegratedLUFS: fp(-10), TruePeakDBTP: fp(-0.5)}
	hot.Evaluate(gates)
	if *hot.OverallPass || *hot.LoudnessPass || *hot.TruePeakPass {
		t.Errorf("out-of-range measurement must fail: %+v", hot)
	}
	// Missing integrated value fails loudness gates; nil true peak passes.
	silent := FileReport{}
	silent.Evaluate(gates)
	if *silent.LoudnessPass || !*silent.TruePeakPass || *silent.OverallPass {
		t.Errorf("silent file: %+v", silent)
	}
	// Errored files are never evaluated.
	bad := FileReport{Error: "broken"}
	bad.Evaluate(gates)
	if bad.OverallPass != nil {
		t.Error("errored file must not get a verdict")
	}
}

func TestJSONNulls(t *testing.T) {
	var buf bytes.Buffer
	rep := FileReport{Path: "x.wav", Channels: 2, SampleRate: 48000}
	if err := WriteJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"integrated_lufs", "short_term_max_lufs", "true_peak_dbtp",
		"loudness_pass", "true_peak_pass", "overall_pass"} {
		v, present := m[k]
		if !present || v != nil {
			t.Errorf("%s: want explicit null, got %v (present=%v)", k, v, present)
		}
	}
	if m["channels"].(float64) != 2 || m["sample_rate"].(float64) != 48000 {
		t.Errorf("channels/sample_rate wrong: %v", m)
	}
}

func TestPrintTable(t *testing.T) {
	reps := []FileReport{
		{Path: "a.wav", Channels: 2, SampleRate: 48000, IntegratedLUFS: fp(-20)},
		{Path: "b.wav", Error: "data chunk truncated"},
	}
	var buf bytes.Buffer
	PrintTable(&buf, reps, Summary{Total: 2, Succeeded: 1, Failed: 1})
	out := buf.String()
	if !strings.Contains(out, "a.wav") || !strings.Contains(out, "ERROR: data chunk truncated") {
		t.Errorf("table missing rows:\n%s", out)
	}
	if !strings.Contains(out, "failed=1") {
		t.Errorf("table missing summary:\n%s", out)
	}
}
