package provenance_test

import (
	"path/filepath"
	"testing"

	"mediacore/internal/provenance"
)

func TestAtomicJSONRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "record.json")
	in := provenance.Record{SchemaVersion: provenance.SchemaVersion, IndexDigest: "abc"}
	if err := provenance.WriteAtomicJSON(path, in); err != nil {
		t.Fatal(err)
	}
	var out provenance.Record
	if err := provenance.ReadJSON(path, &out); err != nil {
		t.Fatal(err)
	}
	if out.IndexDigest != "abc" || out.SchemaVersion != provenance.SchemaVersion {
		t.Fatalf("round trip=%+v", out)
	}
}

func TestConfigDigestStable(t *testing.T) {
	c1, err := provenance.ConfigDigest(map[string]any{"a": 1, "b": "x"})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := provenance.ConfigDigest(map[string]any{"a": 1, "b": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 || len(c1) != 64 {
		t.Fatalf("digests %q %q", c1, c2)
	}
}

func TestFileKeyIsFilesystemSafe(t *testing.T) {
	got := provenance.FileKey("dash:video:1/../x")
	if got == "" || got == "dash:video:1/../x" {
		t.Fatalf("unsafe key %q", got)
	}
	for _, r := range got {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			t.Fatalf("unexpected character %q in %q", r, got)
		}
	}
}
