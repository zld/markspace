package mediakernel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func v1ManifestBytes(t *testing.T) []byte {
	t.Helper()
	old := LegacyManifestV1{
		Version: ManifestV1,
		Seed:    7,
		Entries: []LegacyManifestV1Entry{{
			AssetID:       "old-dialogue",
			Kind:          "dialogue",
			SourceID:      "src-a",
			SourceDigest:  DigestBytes([]byte("legacy-content")),
			TimelineStart: 0,
			TimelineEnd:   48000,
		}},
	}
	b, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeV1AdaptsToCurrent(t *testing.T) {
	m, err := DecodeManifest(v1ManifestBytes(t))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != ManifestCurrent {
		t.Fatalf("adapted version = %d, want %d", m.Version, ManifestCurrent)
	}
	if len(m.Entries) != 1 {
		t.Fatalf("entries = %d", len(m.Entries))
	}
	e := m.Entries[0]
	if e.AssetID != "old-dialogue" || !e.ContentDigest.Equal(DigestBytes([]byte("legacy-content"))) {
		t.Fatalf("v1 fields lost: %+v", e)
	}
	if e.ContainerDigest.Hex != "" {
		t.Fatal("v1 had no container digest; adapted entry must leave it empty")
	}
	if e.Quality.Applicable || e.Quality.Reason == "" {
		t.Fatal("adapted legacy entry must carry a not-applicable quality result")
	}
}

func TestDecodeV2RoundTrip(t *testing.T) {
	m := &Manifest{
		Version: ManifestCurrent, Seed: 9, Stage: StageDone,
		IndexDigest: DigestBytes([]byte("index")),
		Entries: []AssetEntry{{
			AssetID: "a", Kind: "dialogue", SourceID: "s",
			ContentDigest:   DigestBytes([]byte("c")),
			ContainerDigest: DigestBytes([]byte("k")),
			TimelineStart:   10, TimelineEnd: 20,
			Quality: SNRProxy("a", true, 100, 1),
		}},
	}
	if err := m.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.Version != ManifestCurrent || len(back.Entries) != 1 {
		t.Fatalf("v2 round trip wrong: %+v", back)
	}
	if !back.ManifestDigest.Equal(m.ManifestDigest) {
		t.Fatal("manifest digest must survive round trip")
	}
}

func TestDecodeRejectsUnknownVersionAndBadJSON(t *testing.T) {
	if _, err := DecodeManifest([]byte(`{"version":99}`)); err == nil {
		t.Fatal("unknown version must error")
	} else if me, ok := AsMediaError(err); !ok || me.Phase != PhaseMigrate {
		t.Fatalf("want migration-phase error, got %v", err)
	}
	if _, err := DecodeManifest([]byte(`{not json`)); err == nil {
		t.Fatal("invalid JSON must error")
	}
}

func TestManifestValidate(t *testing.T) {
	good := &Manifest{Version: ManifestCurrent, Entries: []AssetEntry{{
		AssetID: "a", TimelineStart: 0, TimelineEnd: 10, ContentDigest: DigestBytes([]byte("x")),
	}}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	badTime := &Manifest{Version: ManifestCurrent, Entries: []AssetEntry{
		{AssetID: "a", TimelineStart: 100, TimelineEnd: 50, ContentDigest: DigestBytes([]byte("x"))},
	}}
	err := badTime.Validate()
	me, ok := AsMediaError(err)
	if !ok || me.Phase != PhaseSpec || me.Time == nil {
		t.Fatalf("time mis-order must produce spec error with time range, got %v", err)
	}

	dup := &Manifest{Version: ManifestCurrent, Entries: []AssetEntry{
		{AssetID: "a", ContentDigest: DigestBytes([]byte("x"))},
		{AssetID: "a", ContentDigest: DigestBytes([]byte("y"))},
	}}
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate asset ids must fail validation")
	}

	noDigest := &Manifest{Version: ManifestCurrent, Entries: []AssetEntry{{AssetID: "b"}}}
	if err := noDigest.Validate(); err == nil {
		t.Fatal("missing content digest must fail validation")
	}

	old := &Manifest{Version: ManifestV1}
	if err := old.Validate(); err == nil {
		t.Fatal("stale version must fail validation")
	}
}

func TestManifestDigestStableAndExcludesItself(t *testing.T) {
	mk := func(stage string) *Manifest {
		return &Manifest{
			Version: ManifestCurrent, Seed: 1, Stage: stage,
			IndexDigest: DigestBytes([]byte("idx")),
			Entries: []AssetEntry{{
				AssetID: "a", ContentDigest: DigestBytes([]byte("c")),
				TimelineStart: 0, TimelineEnd: 9, Quality: SNRProxy("a", false, 0, 0),
			}},
		}
	}
	m1 := mk(StageDone)
	if err := m1.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	// Recomputing on an identical document gives the same digest; the digest
	// field itself is excluded from its own input.
	m2 := mk(StageDone)
	if err := m2.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	if !m1.ManifestDigest.Equal(m2.ManifestDigest) {
		t.Fatal("identical manifests must have identical digests")
	}
	// A stage change must change the digest.
	m3 := mk(StageStage)
	if err := m3.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	if m3.ManifestDigest.Equal(m1.ManifestDigest) {
		t.Fatal("changed stage must change manifest digest")
	}
}

func TestLoadManifestMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadManifest(filepath.Join(dir, "nope.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want os.ErrNotExist, got %v", err)
	}
}
