package audio

import "testing"

func TestParseLoudnessJSON(t *testing.T) {
	stderr := []byte(`some ffmpeg noise
{
	"input_i" : "-21.75",
	"input_tp" : "-18.06",
	"input_lra" : "0.00",
	"input_thresh" : "-31.75",
	"output_i" : "-16.05",
	"output_tp" : "-12.31",
	"output_lra" : "0.00",
	"output_thresh" : "-26.05",
	"normalization_type" : "linear",
	"target_offset" : "0.05"
}
trailing log`)
	got, err := ParseLoudnessJSON(stderr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Integrated != -21.75 || got.TruePeak != -18.06 ||
		got.Range != 0 || got.Threshold != -31.75 {
		t.Fatalf("stats wrong: %+v", got)
	}
}

func TestParseLoudnessJSONMissing(t *testing.T) {
	if _, err := ParseLoudnessJSON([]byte("no json here")); err == nil {
		t.Fatal("missing JSON must error")
	}
}

func TestParseEBUR128(t *testing.T) {
	stderr := []byte(`[Parsed_ebur128_0 @ 0x0] t: 0.4 I: -21.8 LUFS LRA: 0.0 LU FTPK: -18.1 dBFS TPK: -18.1 dBFS
[Parsed_ebur128_0 @ 0x0] t: 2.0 I: -16.2 LUFS LRA: 0.0 LU FTPK: -2.4 dBFS TPK: -2.4 dBFS`)
	got, err := ParseEBUR128(stderr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Last frame wins.
	if got.Integrated != -16.2 || got.TruePeak != -2.4 {
		t.Fatalf("stats wrong: %+v", got)
	}
}

func TestParseEBUR128TPUnit(t *testing.T) {
	// Post-encode measurements label the peak dBTP rather than dBFS.
	stderr := []byte(`[Parsed_ebur128_0 @ 0x0] t: 1.0 I: -15.5 LUFS TPK: -1.3 dBTP`)
	got, err := ParseEBUR128(stderr)
	if err != nil {
		t.Fatal(err)
	}
	if got.TruePeak != -1.3 {
		t.Fatalf("true peak = %v", got.TruePeak)
	}
}

func TestGateDecision(t *testing.T) {
	cases := []struct {
		name string
		s    LoudnessStats
		want bool
	}{
		{"dead center", LoudnessStats{Integrated: -16.0, TruePeak: -2.0}, true},
		{"loud edge", LoudnessStats{Integrated: -15.0, TruePeak: -1.5}, true},
		{"quiet edge", LoudnessStats{Integrated: -17.0, TruePeak: -9.0}, true},
		{"too loud", LoudnessStats{Integrated: -14.5, TruePeak: -2.0}, false},
		{"too quiet", LoudnessStats{Integrated: -17.6, TruePeak: -2.0}, false},
		{"peak over ceiling", LoudnessStats{Integrated: -16.0, TruePeak: -0.8}, false},
		{"peak exactly ceiling", LoudnessStats{Integrated: -16.0, TruePeak: -1.0}, true},
	}
	for _, tc := range cases {
		if got := tc.s.PassesGate(); got != tc.want {
			t.Errorf("%s: gate=%v want %v", tc.name, got, tc.want)
		}
	}
}
