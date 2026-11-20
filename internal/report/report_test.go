package report

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func entry(path string, status Status, integrated, peak float64) Entry {
	return Entry{
		Path:           path,
		SampleRate:     48000,
		Channels:       2,
		ChannelLayout:  "FL-FR",
		IntegratedLUFS: MeasuredLevel(integrated),
		TruePeakDBTP:   MeasuredLevel(peak),
		LoudnessLimit:  -16,
		PeakLimit:      -1,
		Status:         status,
	}
}

func TestBuildSortsStably(t *testing.T) {
	r := Build([]Entry{
		entry("z/last.wav", StatusPass, -20, -3),
		entry("a/first.wav", StatusPass, -20, -3),
		entry("m/mid.wav", StatusPass, -20, -3),
	})
	got := []string{r.Entries[0].Path, r.Entries[1].Path, r.Entries[2].Path}
	want := []string{"a/first.wav", "m/mid.wav", "z/last.wav"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		name    string
		entries []Entry
		want    int
	}{
		{"all pass", []Entry{entry("a", StatusPass, -20, -3)}, 0},
		{"limit fail", []Entry{entry("a", StatusPass, -20, -3), entry("b", StatusLoudnessFail, -10, -3)}, 1},
		{"unmeasured", []Entry{entry("a", StatusNoMeasurement, -70, -3)}, 1},
		{"error wins", []Entry{entry("a", StatusLoudnessFail, -10, -3), {Path: "b", Status: StatusError}}, 3},
	}
	for _, c := range cases {
		if got := Build(c.entries).Summary.ExitCode; got != c.want {
			t.Errorf("%s: exit = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestJSONShapes(t *testing.T) {
	r := Build([]Entry{
		entry("a.wav", StatusPass, -20, -3),
		{Path: "b.wav", Status: StatusNoMeasurement, IntegratedLUFS: NoLevel(), MaxShortTermLUFS: NoLevel(), TruePeakDBTP: MeasuredLevel(-90), LoudnessLimit: -16, PeakLimit: -1},
	})
	var buf bytes.Buffer
	if err := Write(&buf, r, FormatJSON); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var decoded struct {
		Files []map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Files) != 2 {
		t.Fatalf("got %d files", len(decoded.Files))
	}
	// Field set must be fixed across entries.
	wantKeys := []string{"path", "sample_rate", "channels", "channel_layout", "integrated_lufs", "max_short_term_lufs", "true_peak_dbtp", "loudness_limit", "peak_limit", "status", "error_stage", "error"}
	for _, e := range decoded.Files {
		for _, k := range wantKeys {
			if _, ok := e[k]; !ok {
				t.Errorf("missing key %q", k)
			}
		}
	}
	if got := string(decoded.Files[1]["integrated_lufs"]); got != `"-inf"` {
		t.Errorf("unmeasured integrated = %s, want \"-inf\"", got)
	}
	if strings.Contains(buf.String(), "NaN") {
		t.Error("output contains NaN")
	}
	// Both objects must expose exactly the same key set, which is what makes
	// the schema stable for downstream tooling.
	if len(decoded.Files[0]) != len(decoded.Files[1]) {
		t.Errorf("key counts differ: %d vs %d", len(decoded.Files[0]), len(decoded.Files[1]))
	}
}

func TestTableRendersInf(t *testing.T) {
	r := Build([]Entry{{Path: "silence.wav", Status: StatusNoMeasurement, IntegratedLUFS: NoLevel(), MaxShortTermLUFS: NoLevel(), TruePeakDBTP: NoLevel(), LoudnessLimit: -16, PeakLimit: -1}})
	var buf bytes.Buffer
	if err := Write(&buf, r, FormatTable); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "-inf") || !strings.Contains(out, "silence.wav") {
		t.Errorf("unexpected table:\n%s", out)
	}
}

func TestMeasuredLevelRejectsNaN(t *testing.T) {
	l := MeasuredLevel(math.NaN())
	if l.Valid {
		t.Fatal("NaN accepted as a level")
	}
	if _, err := l.MarshalJSON(); err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
}

func TestParseFormat(t *testing.T) {
	if _, err := ParseFormat("xml"); err == nil {
		t.Fatal("unknown format accepted")
	}
	if f, err := ParseFormat("JSON"); err != nil || f != FormatJSON {
		t.Fatalf("JSON: %v %v", f, err)
	}
}
