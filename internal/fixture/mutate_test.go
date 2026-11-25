package fixture_test

import (
	"testing"

	"assetcheck/internal/fixture"
	"assetcheck/internal/wav"
)

func fingerprints(t *testing.T, b []byte) (content, structure, metadata string) {
	t.Helper()
	w, err := wav.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return wav.ContentFingerprint(w), wav.StructureFingerprint(b, w), wav.MetadataFingerprint(b, w)
}

func TestMutationFingerprintMatrix(t *testing.T) {
	good := fixture.NamedFixtures()["good.wav"]
	c0, s0, m0 := fingerprints(t, good)

	contentChanged, err := fixture.MutateContent(good)
	if err != nil {
		t.Fatal(err)
	}
	c1, s1, m1 := fingerprints(t, contentChanged)
	if c1 == c0 {
		t.Fatal("content mutation must change content fingerprint")
	}
	if s1 != s0 || m1 != m0 {
		t.Fatal("content mutation must preserve structure/metadata fingerprints")
	}
	// Replaced asset stays integrity-valid after CRC refresh.
	w, perr := wav.Parse(contentChanged)
	if perr != nil {
		t.Fatal(perr)
	}
	tbl := w.ParseBlockTable(contentChanged)
	if tbl == nil {
		t.Fatal("ABLK missing after content mutation")
	}

	containerChanged, err := fixture.MutateContainer(good)
	if err != nil {
		t.Fatal(err)
	}
	c2, s2, m2 := fingerprints(t, containerChanged)
	if s2 == s0 {
		t.Fatal("container mutation must change structure fingerprint")
	}
	if c2 != c0 || m2 != m0 {
		t.Fatal("container mutation must preserve content/metadata fingerprints")
	}

	metadataChanged, err := fixture.MutateMetadata(good)
	if err != nil {
		t.Fatal(err)
	}
	c3, s3, m3 := fingerprints(t, metadataChanged)
	if m3 == m0 {
		t.Fatal("metadata mutation must change metadata fingerprint")
	}
	if c3 != c0 || s3 != s0 {
		t.Fatal("same-length metadata edit must preserve content/structure fingerprints")
	}
}

func TestGenerateWritesAllFixtures(t *testing.T) {
	dir := t.TempDir()
	paths, err := fixture.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 8 {
		t.Fatalf("expected >=8 fixtures, got %d", len(paths))
	}
}
