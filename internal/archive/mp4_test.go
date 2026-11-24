package archive_test

import (
	"path/filepath"
	"testing"

	"media-archiver/internal/archive"
)

func TestManifestMP4RoundTrip(t *testing.T) {
	payload := []byte(`{"schema_version":1,"status":"completed"}`)
	path := filepath.Join(t.TempDir(), "manifest.mp4")
	if err := archive.WriteManifestArchive(path, payload); err != nil {
		t.Fatal(err)
	}
	got, err := archive.ReadManifestArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch: %q", got)
	}
}
