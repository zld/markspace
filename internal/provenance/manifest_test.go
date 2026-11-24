package provenance

import (
	"path/filepath"
	"testing"

	"mediarun/internal/ebml"
)

func TestManifestRoundtrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manifest.json")
	m := &Manifest{
		SourceSHA256: "src", RTPSourceSHA: "rtp", ParamsSHA256: "pa",
		Errors:      []*ebml.Error{{Code: ebml.CodeTruncatedElement, Offset: 123, Msg: "x"}},
		Unrecovered: 12, AVSyncMS: 0.5, ResultSHA256: "deadbeef",
	}
	if err := Write(p, m); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResultSHA256 != "deadbeef" || got.Unrecovered != 12 ||
		len(got.Errors) != 1 || got.Errors[0].Offset != 123 {
		t.Fatalf("roundtrip %+v", got)
	}
}

func TestStructureVerify(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "result-structure.mp4")
	sha := "0123456789abcdef"
	if err := WriteStructure(p, sha); err != nil {
		t.Fatal(err)
	}
	if err := VerifyStructure(p, sha); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := VerifyStructure(p, "other"); err == nil {
		t.Fatal("digest mismatch must fail verification")
	}
}
