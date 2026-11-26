package continuity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateDemoParallelLayout(t *testing.T) {
	root := t.TempDir()
	fx, err := GenerateDemo(root, DefaultDemoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if fx.SampleCount != 16 { // 8 per source
		t.Fatalf("sample count=%d", fx.SampleCount)
	}
	if len(fx.HLSFiles) != 2 || len(fx.DASHFiles) != 2 {
		t.Fatalf("manifests: hls=%v dash=%v", fx.HLSFiles, fx.DASHFiles)
	}
	for _, rel := range append(append([]string{}, fx.HLSFiles...), fx.DASHFiles...) {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s not written: %v", rel, err)
		}
	}
	main, err := LoadSourceManifest(root, "main", ManifestHLS)
	if err != nil {
		t.Fatal(err)
	}
	if len(main.Segments()) != 4 {
		t.Fatalf("hls segments=%d", len(main.Segments()))
	}
	dash, err := LoadSourceManifest(root, "main", ManifestDASH)
	if err != nil {
		t.Fatal(err)
	}
	if len(dash.Segments()) != 4 {
		t.Fatalf("dash segments=%d", len(dash.Segments()))
	}
}

func TestGenerateDemoSwitchoverHole(t *testing.T) {
	root := t.TempDir()
	if _, err := GenerateDemo(root, SwitchoverDemoConfig()); err != nil {
		t.Fatal(err)
	}
	main, err := LoadSourceManifest(root, "main", ManifestDASH)
	if err != nil {
		t.Fatal(err)
	}
	segs := main.Segments()
	// Seven samples with a hole before the last: 0..150000 then 210000.
	if len(segs) != 7 {
		t.Fatalf("segments=%d", len(segs))
	}
	if segs[5].Start+segs[5].Duration != segs[6].Start {
		t.Fatalf("expected a declared gap: %+v -> %+v", segs[5], segs[6])
	}
}

func TestGenerateDemoDeterministic(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	fa, err := GenerateDemo(a, DefaultDemoConfig())
	if err != nil {
		t.Fatal(err)
	}
	fb, err := GenerateDemo(b, DefaultDemoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(fa.Segments) != len(fb.Segments) {
		t.Fatalf("segment count differs")
	}
	for i := range fa.Segments {
		if fa.Segments[i].File != fb.Segments[i].File || string(fa.Segments[i].Bytes) != string(fb.Segments[i].Bytes) {
			t.Fatalf("segment %d differs between runs", i)
		}
	}
}

func TestDemoUnknownScenarioRejected(t *testing.T) {
	if _, err := GenerateDemo(t.TempDir(), DemoConfig{Scenario: "bogus"}); err == nil || !IsKind(err, KindUsage) {
		t.Fatalf("err=%v", err)
	}
}

func TestDemoPlanTimingIsInteger(t *testing.T) {
	plan, starts := BuildDemoPlan(DefaultDemoConfig())
	for src, segs := range plan {
		if len(segs) != len(starts[src]) {
			t.Fatalf("%s: %d segments vs %d starts", src, len(segs), len(starts[src]))
		}
		for i, specs := range segs {
			var total uint32
			for _, s := range specs {
				if s.Duration == 0 {
					t.Fatalf("%s segment %d has a zero-duration sample", src, i)
				}
				total += s.Duration
			}
			if total == 0 {
				t.Fatalf("%s segment %d is empty", src, i)
			}
		}
	}
}

func TestDemoHLSURIsAreRelative(t *testing.T) {
	root := t.TempDir()
	if _, err := GenerateDemo(root, DefaultDemoConfig()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "main", "video", "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if strings.Contains(line, "/") {
			t.Fatalf("rendition URI should be relative to its directory: %q", line)
		}
	}
}
