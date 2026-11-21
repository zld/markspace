package cli

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func parse(t *testing.T, args ...string) (Options, error) {
	t.Helper()
	var stderr bytes.Buffer
	return Parse(args, &stderr)
}

func TestParseAnalyzeDefaults(t *testing.T) {
	o, err := parse(t, "analyze", "a.wav")
	if err != nil {
		t.Fatal(err)
	}
	if o.Command != "analyze" || len(o.Files) != 1 || o.Files[0] != "a.wav" {
		t.Fatalf("files = %v", o.Files)
	}
	want := struct{ target, tol, peak float64 }{-23, 1, -1}
	got := struct{ target, tol, peak float64 }{
		o.Thresholds.TargetLUFS, o.Thresholds.ToleranceLU, o.Thresholds.MaxTruePeakDBTP,
	}
	if got != want {
		t.Errorf("thresholds = %+v, want %+v", got, want)
	}
	if o.Format != "text" {
		t.Errorf("format = %q", o.Format)
	}
}

// Flags after the file name must still bind to that command rather than
// being treated as extra files.
func TestParseInterleavedFlagsAndFiles(t *testing.T) {
	o, err := parse(t, "batch", "a.wav", "--target-lufs", "-20", "b.wav", "--format", "json", "--workers", "3")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.Files, []string{"a.wav", "b.wav"}) {
		t.Fatalf("files = %v", o.Files)
	}
	if o.Thresholds.TargetLUFS != -20 || o.Format != "json" || o.Workers != 3 {
		t.Fatalf("options = %+v", o)
	}
}

func TestParseBatchInputDir(t *testing.T) {
	o, err := parse(t, "batch", "--input-dir", "dir")
	if err != nil {
		t.Fatal(err)
	}
	if o.InputDir != "dir" || len(o.Files) != 0 {
		t.Fatalf("input dir = %q files = %v", o.InputDir, o.Files)
	}
	if _, err := parse(t, "batch"); err == nil {
		t.Error("batch with no input should fail")
	}
	if _, err := parse(t, "batch", "--input-dir", "dir", "a.wav"); err == nil {
		t.Error("mixing --input-dir with positional files should fail")
	}
}

func TestParseSynth(t *testing.T) {
	o, err := parse(t, "synth", "--out", "x.wav", "--layout", "5.1", "--seconds", "3",
		"--tone-dbfs", "-27", "--encoding", "float32")
	if err != nil {
		t.Fatal(err)
	}
	if o.SynthOut != "x.wav" || o.Synth.Channels != 6 {
		t.Fatalf("synth = %+v", o.Synth)
	}
	if o.Synth.Encoding.String() != "float32" {
		t.Errorf("encoding = %v", o.Synth.Encoding)
	}
	if o.Synth.Seconds != 3 || o.Synth.ToneDBFS != -27 {
		t.Errorf("signal = %+v", o.Synth)
	}
	if _, err := parse(t, "synth"); err == nil {
		t.Error("synth without --out should fail")
	}
	if _, err := parse(t, "synth", "--out", "x.wav", "--encoding", "alaw"); err == nil {
		t.Error("unknown encoding should fail")
	}
	if _, err := parse(t, "synth", "--out", "x.wav", "--layout", "5.1", "--tone-channels", "9"); err == nil {
		t.Error("tone-channels beyond the layout should fail")
	}
}

func TestParseSynthCustomLayout(t *testing.T) {
	o, err := parse(t, "synth", "--out", "x.wav", "--layout", "custom", "--channels", "4",
		"--map", "1,1,0,1")
	if err != nil {
		t.Fatal(err)
	}
	if o.Synth.Channels != 4 {
		t.Errorf("channels = %d, want 4", o.Synth.Channels)
	}
	if o.Mapping != "1,1,0,1" {
		t.Errorf("mapping = %q", o.Mapping)
	}
	if _, err := parse(t, "synth", "--out", "x.wav", "--layout", "custom"); err == nil {
		t.Error("custom layout without --channels should fail")
	}
	// Without a mapping synth cannot verify what it wrote, so refuse up front
	// rather than emitting a file the analyzer rejects.
	if _, err := parse(t, "synth", "--out", "x.wav", "--layout", "custom", "--channels", "4"); err == nil {
		t.Error("custom layout without --map should fail")
	}
	if _, err := parse(t, "synth", "--out", "x.wav", "--layout", "custom", "--channels", "4",
		"--map", "1,1,1"); err == nil {
		t.Error("a mapping shorter than the channel count should fail")
	}
}

func TestParseRejectsBadArguments(t *testing.T) {
	cases := [][]string{
		{},
		{"frobnicate"},
		{"analyze"},
		{"analyze", "a.wav", "b.wav"},
		{"analyze", "a.wav", "--workers", "0"},
		{"analyze", "a.wav", "--workers", "65"},
		{"analyze", "a.wav", "--format", "xml"},
		{"analyze", "a.wav", "--tolerance-lu", "-2"},
		{"analyze", "a.wav", "--target-lufs", "5"},
		{"analyze", "a.wav", "--unknown-flag"},
	}
	for _, args := range cases {
		if _, err := parse(t, args...); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

func TestUsageErrorsAreTyped(t *testing.T) {
	_, err := parse(t, "analyze")
	var ue UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("error %v is not a UsageError", err)
	}
}

func TestHelpIsNotAUnexpectedFailure(t *testing.T) {
	_, err := parse(t, "--help")
	if err == nil {
		t.Error("expected help to return a sentinel")
	}
}
