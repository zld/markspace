package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"archivist/internal/fixture"
	"archivist/internal/pipeline"
)

func writeFixture(t *testing.T, dir, kind string) string {
	t.Helper()
	opts := fixture.DefaultOptions(kind, kind)
	opts.SampleCount = 12
	var s *fixture.Sample
	var err error
	switch kind {
	case fixture.KindTruncated, fixture.KindTruncatedHeader, fixture.KindBadLength:
		s, err = fixture.BuildCorrupt(dir, opts, kind)
	default:
		s, err = fixture.Build(dir, opts)
	}
	if err != nil {
		t.Fatal(err)
	}
	return s.Path
}

func TestCheckExitCodes(t *testing.T) {
	cases := []struct {
		kind string
		want int
	}{
		{fixture.KindNormal, exitOK},
		{fixture.KindWrap, exitOK},
		{fixture.KindRegression, exitOK},
		{fixture.KindTruncated, exitCorrupt},
		{fixture.KindTruncatedHeader, exitCorrupt},
		{fixture.KindBadLength, exitCorrupt},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			root := t.TempDir()
			path := writeFixture(t, root, tc.kind)
			got := cmdCheck([]string{"--out", filepath.Join(root, "work"), path})
			if got != tc.want {
				t.Fatalf("exit = %d want %d", got, tc.want)
			}
		})
	}
}

func TestCheckMissingInput(t *testing.T) {
	root := t.TempDir()
	if code := cmdCheck([]string{"--out", root, filepath.Join(root, "absent.mp4")}); code != exitFailure {
		t.Fatalf("exit = %d", code)
	}
}

func TestCheckRequiresOneInput(t *testing.T) {
	if code := cmdCheck([]string{"--out", t.TempDir()}); code != exitFailure {
		t.Fatalf("exit = %d", code)
	}
}

func TestRunReportIsStable(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, fixture.KindNormal)
	run := func() *report {
		r, err := pipeline.New(pipeline.Options{
			QueueCapacity: 1,
			WorkDir:       filepath.Join(root, "work"),
			ChunkSamples:  1,
		})
		if err != nil {
			t.Fatal(err)
		}
		results, err := r.Run(context.Background(), []string{path})
		if err != nil {
			t.Fatal(err)
		}
		return &report{Command: "run", Results: results}
	}
	a := run()
	b := run()
	if a.Results[0].OutputSHA256 != b.Results[0].OutputSHA256 {
		t.Fatal("output digest changed between runs")
	}
	if len(b.Results[0].ExecutedStages) != 0 {
		t.Fatalf("second run executed %v", b.Results[0].ExecutedStages)
	}
}

func TestDemoProducesReport(t *testing.T) {
	root := t.TempDir()
	code := cmdDemo([]string{"--dir", root, "--queue", "2"})
	if code != exitOK {
		t.Fatalf("demo exit = %d", code)
	}
	data, err := os.ReadFile(filepath.Join(root, "inputs", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m fixture.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Samples) != 10 {
		t.Fatalf("manifest samples = %d", len(m.Samples))
	}
}

// TestDemoRepeatedSameDirIsStable guards running the documented acceptance
// command more than once against the same --dir. Products left by the first
// invocation must not collapse the cold-first vs resume comparison into a
// spurious timing failure or change any digest.
func TestDemoRepeatedSameDirIsStable(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 2; i++ {
		if code := cmdDemo([]string{"--dir", root, "--queue", "2"}); code != exitOK {
			t.Fatalf("demo run %d exit = %d", i+1, code)
		}
	}

	outPath := filepath.Join(root, "work", "output", "normal.mp4")
	first, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if code := cmdDemo([]string{"--dir", root, "--queue", "2"}); code != exitOK {
		t.Fatalf("demo run 3 exit = %d", code)
	}
	third, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	// Resetting the work trees must still reproduce byte-identical output.
	if !bytes.Equal(first, third) {
		t.Fatal("output/normal.mp4 differs across repeated demo runs")
	}
}
