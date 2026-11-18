package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dashforge/internal/model"
	"dashforge/internal/mpd"
	"dashforge/internal/segments"
)

func mkRendition(id string, bw int, root string, starts []int64, durs []int64) *model.RenditionManifest {
	segs := make([]model.SegmentInfo, len(starts))
	for i := range starts {
		segs[i] = model.SegmentInfo{Number: i + 1, Start: starts[i], Duration: durs[i]}
	}
	last := segs[len(segs)-1]
	return &model.RenditionManifest{
		ID: id, Bandwidth: bw, Timescale: 1000, Media: id + "/$Number$.m4s",
		SegmentRoot: root, Segments: segs, Duration: last.Start + last.Duration,
	}
}

func alignedManifest() *model.Manifest {
	// Both renditions: boundaries at 0/4000/8000, 8s duration.
	return &model.Manifest{Timescale: 1000, Renditions: []*model.RenditionManifest{
		mkRendition("lo", 100, "root", []int64{0, 4000}, []int64{4000, 4000}),
		mkRendition("hi", 200, "root", []int64{0, 4000}, []int64{4000, 4000}),
	}}
}

func expand(t *testing.T, man *model.Manifest) []segments.RenditionResult {
	t.Helper()
	res, issues := segments.Expand(man)
	if len(issues) != 0 {
		t.Fatalf("unexpected expansion issues: %v", issues)
	}
	return res
}

func TestModelHappyPath(t *testing.T) {
	man := alignedManifest()
	findings := Model(man, expand(t, man), Options{ToleranceMs: 20})
	if len(findings) != 0 {
		t.Fatalf("unexpected findings: %v", findings)
	}
}

func TestNegativeToleranceRejected(t *testing.T) {
	man := alignedManifest()
	findings := Model(man, nil, Options{ToleranceMs: -1})
	if len(findings) != 1 || findings[0].Kind != "tolerance" {
		t.Fatalf("expected tolerance finding, got %v", findings)
	}
}

func TestDurationDriftReported(t *testing.T) {
	man := alignedManifest()
	// Reference lasts 8s; second rendition ends at 8.5s -> 500ms drift.
	man.Renditions[1].Segments[1].Duration = 4500
	man.Renditions[1].Duration = 8500
	findings := Model(man, expand(t, man), Options{ToleranceMs: 20})
	if !hasKind(findings, "duration_drift") {
		t.Fatalf("expected duration_drift, got %v", findings)
	}
	d := findKind(findings, "duration_drift")
	if d.ErrorMs < 499 || d.ErrorMs > 501 {
		t.Fatalf("drift error = %v, want ~500ms", d.ErrorMs)
	}
	// A generous tolerance makes the same model pass.
	if len(Model(man, expand(t, man), Options{ToleranceMs: 1000})) != 0 {
		t.Fatal("model should pass within generous tolerance")
	}
}

func TestBoundaryMisalignmentReported(t *testing.T) {
	man := alignedManifest()
	// Second rendition boundary at 3990ms instead of 4000: 10ms off.
	man.Renditions[1].Segments[0].Duration = 3990
	man.Renditions[1].Segments[1].Start = 3990
	findings := Model(man, expand(t, man), Options{ToleranceMs: 5})
	if !hasKind(findings, "boundary_misalignment") {
		t.Fatalf("expected boundary finding, got %v", findings)
	}
	b := findKind(findings, "boundary_misalignment")
	if b.Rendition != "hi" || b.BoundaryMs != 3990 || b.ErrorMs != 10 {
		t.Fatalf("unexpected boundary finding: %+v", b)
	}
	// Within tolerance -> clean.
	if len(Model(man, expand(t, man), Options{ToleranceMs: 20})) != 0 {
		t.Fatal("aligned-within-tolerance model should pass")
	}
}

func TestContiguityAndReferences(t *testing.T) {
	man := alignedManifest()
	// Punch a 500ms hole between the two fragments of rendition hi.
	man.Renditions[1].Segments[1].Start = 4500
	findings := Model(man, expand(t, man), Options{ToleranceMs: 20})
	if !hasKind(findings, "timeline_gap") {
		t.Fatalf("expected timeline_gap, got %v", findings)
	}

	dir := t.TempDir()
	man = alignedManifest()
	man.Renditions[0].SegmentRoot = dir
	missing := Model(man, expand(t, man), Options{ToleranceMs: 20, CheckFiles: true})
	if !hasKind(missing, "unresolved_reference") {
		t.Fatalf("expected unresolved references, got %v", missing)
	}
	for _, rel := range []string{"lo/1.m4s", "lo/2.m4s"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ok := Model(man, expand(t, man), Options{ToleranceMs: 20, CheckFiles: true})
	// Only lo files exist; hi still unresolved, but lo must produce no findings.
	for _, f := range ok {
		if f.Rendition == "lo" {
			t.Fatalf("lo should be clean with files present: %v", f)
		}
	}
}

func TestMPDCrossCheck(t *testing.T) {
	man := alignedManifest()
	doc := mpd.Build(man)
	path := filepath.Join(t.TempDir(), "a.mpd")
	if err := mpd.WriteFile(path, doc); err != nil {
		t.Fatal(err)
	}
	if findings := MPD(man, path); len(findings) != 0 {
		t.Fatalf("fresh MPD should cross-check: %v", findings)
	}
	// Corrupt the file: invalid XML.
	bad := filepath.Join(t.TempDir(), "bad.mpd")
	if err := os.WriteFile(bad, []byte("<MPD>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if findings := MPD(man, bad); !hasKind(findings, "mpd_parse") {
		t.Fatalf("expected parse finding, got %v", findings)
	}
	if findings := MPD(man, filepath.Join(t.TempDir(), "missing.mpd")); !hasKind(findings, "mpd_read") {
		t.Fatalf("expected read finding, got %v", findings)
	}
}

func TestFindingsErrorIsSorted(t *testing.T) {
	fs := Findings{
		{Rendition: "b", Kind: "z", Detail: "1"},
		{Rendition: "a", Kind: "z", Detail: "2"},
	}
	err := fs.Error()
	if !strings.Contains(err, "rendition \"a\"") || strings.Index(err, "rendition \"a\"") > strings.Index(err, "rendition \"b\"") {
		t.Fatalf("findings not sorted: %s", err)
	}
}

func hasKind(fs Findings, kind string) bool {
	return findKind(fs, kind) != nil
}

func findKind(fs Findings, kind string) *Finding {
	for i := range fs {
		if fs[i].Kind == kind {
			return &fs[i]
		}
	}
	return nil
}
