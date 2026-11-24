package provenance

import (
	"strings"
	"testing"

	"example.com/media-kernel/media"
)

func TestHashBytesAndRange(t *testing.T) {
	data := []byte("media-kernel")
	if got := HashBytes(data); len(got) != 64 || got != HashBytes(data) {
		t.Fatalf("hash not stable: %q", got)
	}
	got, err := HashRange([]byte("abcdef"), media.ByteRange{Start: 1, End: 4})
	if err != nil {
		t.Fatal(err)
	}
	if want := HashBytes([]byte("bcd")); got != want {
		t.Fatalf("range hash=%s want %s", got, want)
	}
	if _, err := HashRange(data, media.ByteRange{Start: -1, End: 2}); err == nil {
		t.Fatal("expected invalid range error")
	}
}

func TestNewManifestRecordsVersions(t *testing.T) {
	m, err := NewManifest(Source{Name: "generated", SHA256: HashBytes(nil)}, struct{ MaxMemory string }{MaxMemory: "8MiB"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.ParseVersion, "go-mp4-v1.4.1") || m.ParametersHash == "" {
		t.Fatalf("manifest metadata incomplete: %+v", m)
	}
}
