package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"medpipe/internal/apperr"
)

func writeMedia(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("ftyp"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func validManifest() string {
	return `{
	  "schema_version": 1,
	  "tracks": [
	    {"id":"a","path":"a.mp4"},
	    {"id":"b","path":"b.mov","duration_ticks":10}
	  ],
	  "switches":[{"track":"a","sample":2}],
	  "dependencies":[{"stage":"mix","requires":["a","b"]}]
	}`
}

func TestParseValidAndCanonicalHash(t *testing.T) {
	dir := t.TempDir()
	writeMedia(t, dir, "a.mp4")
	writeMedia(t, dir, "b.mov")
	m, err := Parse([]byte(validManifest()), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tracks) != 2 || m.Parameters.WrapBits != 32 {
		t.Fatalf("normalization failed: %#v", m.Parameters)
	}
	h1, err := m.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := m.CanonicalHash()
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("bad hash stability: %q %q", h1, h2)
	}
}

func TestParseRejectsPathTraversalAndMissing(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"tracks":[{"id":"a","path":"../escape.mp4"}]}`)
	_, err := Parse(data, dir, false)
	pe, ok := apperr.As(err)
	if !ok || pe.Code != "E_PATH_ESCAPE" {
		t.Fatalf("err=%v pe=%#v", err, pe)
	}

	data = []byte(`{"tracks":[{"id":"a","path":"missing.mp4"}]}`)
	_, err = Parse(data, dir, true)
	if pe, ok := apperr.As(err); !ok || pe.Code != "E_TRACK_FILE_MISSING" {
		t.Fatalf("missing err=%v", err)
	}
}

func TestParseRejectsNegativeDurationMissingTrackAndCycle(t *testing.T) {
	dir := t.TempDir()
	writeMedia(t, dir, "a.mp4")

	_, err := Parse([]byte(`{"tracks":[{"id":"a","path":"a.mp4","duration_ticks":-1}]}`), dir, true)
	if pe, ok := apperr.As(err); !ok || pe.Code != "E_MANIFEST_DURATION" {
		t.Fatalf("duration err=%v", err)
	}

	bad := strings.Replace(validManifest(), `"track":"a"`, `"track":"ghost"`, 1)
	writeMedia(t, dir, "b.mov")
	_, err = Parse([]byte(bad), dir, true)
	if pe, ok := apperr.As(err); !ok || pe.Code != "E_TRACK_MISSING" {
		t.Fatalf("missing track ref err=%v", err)
	}

	cycle := `{"tracks":[{"id":"a","path":"a.mp4"}],
	  "dependencies":[{"stage":"x","requires":["y"]},{"stage":"y","requires":["x"]}]}`
	_, err = Parse([]byte(cycle), dir, true)
	if pe, ok := apperr.As(err); !ok || pe.Code != "E_CYCLE" {
		t.Fatalf("cycle err=%v", err)
	}
}
