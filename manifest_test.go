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

func buildManifest(seed int64, id string) *Manifest {
	return &Manifest{
		Version: ManifestCurrent, Seed: seed, Stage: StageDone,
		IndexDigest: DigestBytes([]byte("idx")),
		Entries: []AssetEntry{{
			AssetID: id, Kind: "dialogue", SourceID: "src-a",
			ContentDigest: DigestBytes([]byte("content-" + id)),
			TimelineStart: 0, TimelineEnd: 48000,
			Quality: SNRProxy(id, true, 100, 4),
		}},
	}
}

func TestMigrationHappyPath(t *testing.T) {
	dir := t.TempDir()
	mg := NewMigrator(dir, "manifest.json")
	m := buildManifest(7, "a")
	rep, err := mg.Run(m)
	if err != nil || !rep.Committed || rep.Stage != StageDone {
		t.Fatalf("happy run failed: rep=%+v err=%v", rep, err)
	}
	if m.ManifestDigest.Hex == "" {
		t.Fatal("committed manifest must carry a digest")
	}
	if mg.HasRollback() {
		t.Fatal("rollback point must be removed after commit")
	}
	if _, err := os.Stat(mg.tempPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temp file must not linger after commit")
	}
	loaded, err := LoadManifest(mg.ManifestPath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Stage != StageDone || !loaded.ManifestDigest.Equal(m.ManifestDigest) {
		t.Fatalf("loaded manifest mismatch: %+v", loaded)
	}
}

func TestMigrationInterruptKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	mg := NewMigrator(dir, "manifest.json")
	original := buildManifest(1, "orig")
	if _, err := mg.Run(original); err != nil {
		t.Fatal(err)
	}
	wantBytes, err := os.ReadFile(mg.ManifestPath())
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := original.ManifestDigest

	for _, stage := range []string{StageValidate, StageStage, StageCommit} {
		mg.SetInterruptHook(func(s string) error {
			if s == stage {
				return errors.New("simulated crash")
			}
			return nil
		})
		next := buildManifest(2, "next-"+stage)
		rep, err := mg.Run(next)
		if err == nil {
			t.Fatalf("hook at %s must interrupt", stage)
		} else if me, ok := AsMediaError(err); !ok || me.Phase != PhaseMigrate {
			t.Fatalf("interrupt must be a migration error, got %v", err)
		}
		if rep.Committed {
			t.Fatalf("interrupted run at %s must not report committed", stage)
		}
		gotBytes, rerr := os.ReadFile(mg.ManifestPath())
		if rerr != nil || !bytesEqual(gotBytes, wantBytes) {
			t.Fatalf("after interrupt at %s original manifest changed", stage)
		}
		if _, err := os.Stat(mg.tempPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temp file must be cleaned up after %s interrupt", stage)
		}
		loaded, _ := LoadManifest(mg.ManifestPath())
		if !loaded.ManifestDigest.Equal(wantDigest) {
			t.Fatalf("digest changed after interrupt at %s", stage)
		}
		// Validate-stage interrupt happens before the rollback point exists.
		if stage == StageValidate && mg.HasRollback() {
			t.Fatal("validate interrupt must not leave a rollback point")
		}
	}
}

func TestMigrationRestoreAfterInterrupt(t *testing.T) {
	dir := t.TempDir()
	mg := NewMigrator(dir, "manifest.json")
	original := buildManifest(1, "orig")
	if _, err := mg.Run(original); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(mg.ManifestPath())

	mg.SetInterruptHook(func(s string) error {
		if s == StageStage {
			return errors.New("boom")
		}
		return nil
	})
	if _, err := mg.Run(buildManifest(2, "next")); err == nil {
		t.Fatal("expected interrupt")
	}
	if !mg.HasRollback() {
		t.Fatal("stage interrupt must leave a rollback point")
	}
	if err := mg.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(mg.ManifestPath())
	if err != nil || !bytesEqual(got, want) {
		t.Fatal("restore must reinstate the exact original bytes")
	}
	if mg.HasRollback() {
		t.Fatal("restore must consume the rollback point")
	}
}

func TestMigrationFirstRunInterruptThenCommit(t *testing.T) {
	dir := t.TempDir()
	mg := NewMigrator(dir, "manifest.json")
	mg.SetInterruptHook(func(s string) error {
		if s == StageStage {
			return errors.New("boom")
		}
		return nil
	})
	if _, err := mg.Run(buildManifest(1, "a")); err == nil {
		t.Fatal("expected interrupt")
	}
	if _, err := os.Stat(mg.ManifestPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted first run must not create the target manifest")
	}
	if err := mg.Restore(); err != nil {
		t.Fatalf("restore on empty marker: %v", err)
	}
	mg.SetInterruptHook(nil)
	rep, err := mg.Run(buildManifest(1, "a"))
	if err != nil || !rep.Committed {
		t.Fatalf("retry after first-run interrupt must commit: %v", err)
	}
}

func TestMigrationRepeatDigestStability(t *testing.T) {
	dir := t.TempDir()
	mg := NewMigrator(dir, "manifest.json")
	m1 := buildManifest(7, "a")
	if _, err := mg.Run(m1); err != nil {
		t.Fatal(err)
	}
	// A second identical migration produces the same manifest digest.
	m2 := buildManifest(7, "a")
	rep, err := mg.Run(m2)
	if err != nil || !rep.Committed {
		t.Fatalf("second run: %v", err)
	}
	if !m2.ManifestDigest.Equal(m1.ManifestDigest) {
		t.Fatal("identical manifests across runs must share a digest")
	}
	_ = rep
}

func TestMigrationRejectsInvalidNext(t *testing.T) {
	dir := t.TempDir()
	mg := NewMigrator(dir, "manifest.json")
	bad := &Manifest{Version: ManifestCurrent, Entries: []AssetEntry{{
		AssetID: "x", TimelineStart: 9, TimelineEnd: 1, ContentDigest: DigestBytes([]byte("c")),
	}}}
	rep, err := mg.Run(bad)
	if err == nil {
		t.Fatal("invalid manifest must not migrate")
	}
	if rep.Stage != StageValidate {
		t.Fatalf("failure stage = %s, want validate", rep.Stage)
	}
	if _, err := os.Stat(mg.ManifestPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed validation must leave no target file")
	}
	if mg.HasRollback() {
		t.Fatal("validation failure must leave no rollback point")
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
