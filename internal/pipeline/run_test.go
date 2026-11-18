package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"subtitle/internal/cue"
	"subtitle/internal/report"
	"subtitle/internal/timebase"
)

const srtFixture = "1\n00:00:00,000 --> 00:00:02,000\nopening line\n\n" +
	"2\n00:00:02,002 --> 00:00:04,000\nsecond cue\n\n" +
	"3\n00:00:05,005 --> 00:00:06,000\nthird cue\nfirst overlap\n\n" +
	"4\n00:00:05,500 --> 00:00:05,900\nfourth cue\nsecond overlap\n"

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func demoOptions(t *testing.T, strategy string) Options {
	in := writeTemp(t, "scene.srt", srtFixture)
	rate, err := timebase.ParseRate("24000/1001")
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		InputPath:  in,
		OutputPath: filepath.Join(filepath.Dir(in), "scene.ass"),
		ToFormat:   cue.FormatASS,
		Roundtrip:  cue.FormatSRT,
		Rate:       rate,
		Strategy:   strategy,
	}
}

func TestRunDropLaterDemoShape(t *testing.T) {
	opts := demoOptions(t, "drop-later")
	res := Run(opts)
	if res.Error != "" {
		t.Fatalf("run failed: %s", res.Error)
	}
	if res.InputCount != 4 || res.OutputCount != 3 || res.Dropped != 1 {
		t.Fatalf("counts = %+v", res)
	}
	if res.Encoding != "utf-8" {
		t.Fatalf("encoding = %q", res.Encoding)
	}
	var third report.CueLine
	for _, c := range res.Cues {
		if c.Cue == 3 {
			third = c
		}
		if c.Cue == 4 && !c.Dropped {
			t.Fatal("cue 4 must be dropped")
		}
	}
	if third.StartFrame != 120 || third.EndFrame != 144 || third.Dropped {
		t.Fatalf("cue 3 = %+v, want frames [120,144)", third)
	}
	if res.MaxAbsDeltaMS > 5 || res.MaxAbsDeltaMS == 0 {
		t.Fatalf("max delta = %d, expected nonzero within 5ms", res.MaxAbsDeltaMS)
	}
	if _, err := os.Stat(opts.OutputPath); err != nil {
		t.Fatalf("output not written: %v", err)
	}
}

func TestRunRefusesOverwriteAndInputCollision(t *testing.T) {
	opts := demoOptions(t, "drop-later")
	if res := Run(opts); res.Error != "" {
		t.Fatal(res.Error)
	}
	// Second run without force must refuse.
	res := Run(opts)
	if res.Error == "" || !strings.Contains(res.Error, "already exists") {
		t.Fatalf("expected overwrite refusal, got %q", res.Error)
	}
	// Forcing succeeds.
	opts.Force = true
	if res := Run(opts); res.Error != "" {
		t.Fatalf("forced overwrite failed: %s", res.Error)
	}
	// Output may never target the input path itself.
	opts.OutputPath = opts.InputPath
	opts.Force = true
	if res := Run(opts); res.Error == "" {
		t.Fatal("overwriting the input file must be rejected")
	}
}

func TestRunKeepStrategy(t *testing.T) {
	opts := demoOptions(t, "keep")
	res := Run(opts)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if res.OutputCount != 4 || res.Dropped != 0 {
		t.Fatalf("keep must preserve all cues: %+v", res)
	}
}

func TestRunBadFileReportsError(t *testing.T) {
	opts := demoOptions(t, "drop-later")
	opts.InputPath = filepath.Join(t.TempDir(), "missing.srt")
	res := Run(opts)
	if res.Error == "" {
		t.Fatal("missing file must produce a report error")
	}
}

func TestBatchContinuesAfterFailure(t *testing.T) {
	good := demoOptions(t, "drop-later")
	dir := filepath.Dir(good.InputPath)
	bad := filepath.Join(dir, "broken.srt")
	if err := os.WriteFile(bad, []byte("1\nbroken timing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := DiscoverInputs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("discovered %d paths", len(paths))
	}
	optionSets := make([]Options, len(paths))
	for i, p := range paths {
		o := good
		o.InputPath = p
		o.OutputPath = filepath.Join(dir, "out", "o"+string(rune('0'+i))+".ass")
		o.Force = true
		optionSets[i] = o
	}
	results := Batch(optionSets, 2)
	failed, ok := 0, 0
	for _, r := range results {
		if r.Error != "" {
			failed++
		} else {
			ok++
		}
	}
	if failed != 1 || ok != 1 {
		t.Fatalf("want one failure one success, got %d/%d", failed, ok)
	}
	if results[0].Path > results[1].Path {
		t.Fatal("results must be sorted by path")
	}
}

func TestFileSizeLimit(t *testing.T) {
	big := filepath.Join(t.TempDir(), "big.srt")
	if err := os.WriteFile(big, make([]byte, MaxFileSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := demoOptions(t, "drop-later")
	opts.InputPath = big
	if res := Run(opts); res.Error == "" {
		t.Fatal("oversized file must be rejected")
	}
}
