package mpd_test

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dashgen/internal/align"
	"dashgen/internal/demo"
	"dashgen/internal/manifest"
	"dashgen/internal/mpd"
	"dashgen/internal/packager"
)

const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

func buildDemoDoc(t *testing.T) *manifest.MPD {
	t.Helper()
	p := demo.Build()
	report, err := align.Build(p)
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	pkg, err := packager.Build(p, report, "seg-$RepresentationID$-$Number%05d$.m4s", "")
	if err != nil {
		t.Fatalf("package: %v", err)
	}
	return pkg.MPD
}

func TestMarshalRoundTrip(t *testing.T) {
	doc := buildDemoDoc(t)
	data, err := mpd.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), xmlHeader) {
		t.Fatalf("missing XML header: %q", data[:40])
	}
	var parsed manifest.MPD
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("generated XML does not parse: %v", err)
	}
	sets := parsed.Periods[0].AdaptationSets
	if len(sets[0].Representations) != 3 {
		t.Fatalf("round-trip reps = %d, want 3", len(sets[0].Representations))
	}
}

func TestMarshalStable(t *testing.T) {
	doc := buildDemoDoc(t)
	a, _ := mpd.Marshal(doc)
	b, _ := mpd.Marshal(doc)
	if string(a) != string(b) {
		t.Fatal("repeated marshalling must be byte-identical")
	}
}

func TestMarshalEscapesText(t *testing.T) {
	doc := &manifest.MPD{
		Profiles:                  "p&q<>\"'",
		Type:                      "static",
		MediaPresentationDuration: "PT1S",
		MinBufferTime:             "PT1S",
		Periods: []manifest.Period{{
			ID: "P0",
			AdaptationSets: []manifest.AdaptationSet{{
				ID:          0,
				ContentType: "video",
				MimeType:    manifest.VideoMimeType,
				Representations: []manifest.Representation{{
					ID:        `a&b<>"'`,
					Bandwidth: 1,
				}},
			}},
		}},
	}
	data, err := mpd.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	// encoding/xml emits & < > as named entities and quotes as numeric
	// character references; all five special characters must be escaped.
	for _, raw := range []string{"&amp;", "&lt;", "&gt;", "&#34;", "&#39;"} {
		if !strings.Contains(out, raw) {
			t.Errorf("output missing escaped form %s:\n%s", raw, out)
		}
	}
	if strings.Contains(out, "<>") {
		t.Error("raw angle brackets leaked into output")
	}
	if err := xml.Unmarshal(data, &manifest.MPD{}); err != nil {
		t.Errorf("escaped document does not parse: %v", err)
	}
}

func TestMarshalNil(t *testing.T) {
	if _, err := mpd.Marshal(nil); err == nil {
		t.Fatal("nil document must error")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	doc := buildDemoDoc(t)
	path := filepath.Join(t.TempDir(), "nested", "stream.mpd")
	if err := mpd.WriteFile(path, doc); err != nil {
		t.Fatalf("write: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	want, _ := mpd.Marshal(doc)
	if string(written) != string(want) {
		t.Fatal("file content differs from Marshal output")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".dashgen-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestWriteFileBadTarget(t *testing.T) {
	doc := buildDemoDoc(t)
	// A path whose parent is an existing file cannot be created.
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := mpd.WriteFile(filepath.Join(blocker, "stream.mpd"), doc)
	if err == nil {
		t.Fatal("writing under a file path must fail")
	}
}
