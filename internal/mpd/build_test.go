package mpd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dashforge/internal/model"
)

func sampleManifest() *model.Manifest {
	return &model.Manifest{
		Title:     "demo",
		Timescale: 90000,
		Renditions: []*model.RenditionManifest{{
			ID: "v0", Width: 640, Height: 360, Bandwidth: 800_000,
			Codec: "avc1.64001e", FrameRate: "30/1", Timescale: 90000,
			Media:          "v/$RepresentationID$/seg-$Number$.m4s",
			Initialization: "v/$RepresentationID$/init.mp4",
			SegmentRoot:    "root",
			Segments: []model.SegmentInfo{
				{Number: 1, Start: 0, Duration: 360000},
				{Number: 2, Start: 360000, Duration: 180000},
			},
			Timeline: []model.TimelineEntry{
				{Start: 0, Duration: 360000, Repeat: 0},
				{Start: 360000, Duration: 180000, Repeat: 0},
			},
			Duration: 540000,
		}},
	}
}

func TestBuildAndMarshalDeterministic(t *testing.T) {
	doc := Build(sampleManifest())
	a, err := Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(Build(sampleManifest()))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("marshaling is not deterministic")
	}
	xml := string(a)
	for _, want := range []string{
		`xmlns="urn:mpeg:dash:schema:mpd:2011"`,
		`type="static"`,
		`mediaPresentationDuration="PT6S"`,
		`<SegmentTemplate timescale="90000"`,
		`<S t="0" d="360000"></S>`,
		`<S t="360000" d="180000"></S>`,
		`<Representation id="v0" bandwidth="800000"`,
		`codecs="avc1.64001e"`,
		`media="v/$RepresentationID$/seg-$Number$.m4s"`,
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("XML missing %q:\n%s", want, xml)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		ticks int64
		ts    int
		want  string
	}{
		{540000, 90000, "PT6S"},
		{0, 90000, "PT0S"},
		{1, 2, "PT0.5S"},
		{3723, 1000, "PT3.723S"},
		{3661500, 1000, "PT1H1M1.5S"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.ticks, c.ts); got != c.want {
			t.Errorf("FormatDuration(%d,%d) = %q, want %q", c.ticks, c.ts, got, c.want)
		}
	}
}

func TestAttributeEscaping(t *testing.T) {
	man := sampleManifest()
	man.Renditions[0].Media = `v/a&b/<x>/"q"/$Number$.m4s`
	doc := Build(man)
	out, err := Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	xml := string(out)
	for _, banned := range []string{`<x>`, `"q"`} {
		if strings.Contains(xml, banned) {
			t.Errorf("unescaped markup leaked into XML: %s", xml)
		}
	}
	// Go escapes attribute quotes as &#34; (legal XML numeric entity).
	for _, want := range []string{"&amp;", "&lt;", "&#34;"} {
		if !strings.Contains(xml, want) {
			t.Errorf("expected escaped entity %q in: %s", want, xml)
		}
	}
}

func TestWriteFileAtomicAndPreservesTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "out.mpd")
	if err := WriteFile(target, Build(sampleManifest())); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Marshal(Build(sampleManifest()))
	if string(got) != string(want) {
		t.Fatal("file content differs from Marshal output")
	}

	// A failed rename (target path is an existing directory) must leave any
	// previously written file at that path intact.
	original, _ := os.ReadFile(target)
	blocker := filepath.Join(dir, "blocker")
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(blocker, Build(sampleManifest())); err == nil {
		t.Fatal("expected rename failure when target is a directory")
	}
	after, _ := os.ReadFile(target)
	if string(after) != string(original) {
		t.Fatal("existing target was modified by failed write")
	}
}
