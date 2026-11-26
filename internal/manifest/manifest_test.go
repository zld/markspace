package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"replaypipe/internal/store"
	"replaypipe/internal/timebase"
)

func v2Sample(t *testing.T) []byte {
	t.Helper()
	m := &Manifest{
		Version:             Version,
		MediaContentDigest:  "aaaa",
		MediaEnvelopeDigest: "bbbb",
		EventDigest:         "cccc",
		Entries: []Entry{{
			SliceID: "clip-01", EventID: "srt-0001", EventKind: "subtitle",
			EventStartNS: 2_000_000_000, EventEndNS: 4_000_000_000,
			AlignedStartNS: 1_000_000_000, AlignedEndNS: 4_000_000_000,
			Path: "clip-01.mp4", Size: 1234, SHA256: "deadbeef",
			AssetDigest: "feedface",
			Quality:     Quality{Width: 1280, Height: 720, FPS: 30, GateOK: true, BudgetOK: true, Representation: "native"},
		}},
	}
	data, e := Encode(m)
	if e != nil {
		t.Fatal(e)
	}
	return data
}

func TestAdapterReadsV2(t *testing.T) {
	data := v2Sample(t)
	m, e := Adapter{}.Read(data)
	if e != nil {
		t.Fatal(e)
	}
	if m.Version != Version || len(m.Entries) != 1 {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Entries[0].AssetDigest != "feedface" {
		t.Fatal("asset digest lost")
	}
}

func TestAdapterRejectsUnknownVersion(t *testing.T) {
	_, e := Adapter{}.Read([]byte(`{"version":99}`))
	if e == nil || e.Code != timebase.CodeManifestVersion {
		t.Fatalf("want MANIFEST_VERSION_UNSUPPORTED, got %v", e)
	}
}

func TestAdapterDetectsDigestTamper(t *testing.T) {
	data := v2Sample(t)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	entries := m["entries"].([]any)
	entries[0].(map[string]any)["size"] = float64(9999)
	tampered, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	_, e := Adapter{}.Read(tampered)
	if e == nil || e.Code != timebase.CodeDigestMismatch {
		t.Fatalf("want DIGEST_MISMATCH, got %v", e)
	}
}

func TestV1NormalisationExact(t *testing.T) {
	legacy := []byte(`{"version":1,"media_content_digest":"aaaa","clips":[
		{"id":"c1","event_id":"e1","start_sec":2.1,"end_sec":4.3,"path":"c1.mp4","bytes":10,"sha256":"aa","width":1280,"height":720,"fps":30}
	]}`)
	m, e := Adapter{}.Read(legacy)
	if e != nil {
		t.Fatal(e)
	}
	if m.Version != Version {
		t.Fatalf("version = %d", m.Version)
	}
	// 2.1 s must be exactly 2_100_000_000 ns, not 2_099_999_9xx from float math.
	if got := m.Entries[0].EventStartNS; got != 2_100_000_000 {
		t.Fatalf("start = %d, want 2100000000", got)
	}
	if got := m.Entries[0].EventEndNS; got != 4_300_000_000 {
		t.Fatalf("end = %d, want 4300000000", got)
	}
	if m.Entries[0].Quality.Representation != "migrated-v1" {
		t.Fatalf("representation = %q", m.Entries[0].Quality.Representation)
	}
}

func TestValidateRejectsBadEntry(t *testing.T) {
	m := &Manifest{Version: Version, MediaContentDigest: "a"}
	m.Entries = []Entry{{SliceID: "s", EventID: "e", SHA256: "x", Quality: Quality{GateOK: false}}}
	if e := m.Validate(); e == nil || e.Code != timebase.CodeManifestInvalid {
		t.Fatalf("want MANIFEST_INVALID, got %v", e)
	}
}

func TestMigrateIsAtomicAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	legacy := []byte(`{"version":1,"media_content_digest":"aaaa","clips":[
		{"id":"c1","event_id":"e1","start_sec":2.0,"end_sec":4.0,"path":"c1.mp4","bytes":10,"sha256":"aa","width":1280,"height":720,"fps":30}
	]}`)
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	res, e := Migrate(path, MigrateOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if !res.Migrated || res.Rollback == "" {
		t.Fatalf("result = %+v", res)
	}
	rollback, err := os.ReadFile(res.Rollback)
	if err != nil {
		t.Fatal(err)
	}
	if string(rollback) != string(legacy) {
		t.Fatal("rollback is not the original manifest")
	}
	next, e := Adapter{}.Read(mustRead(t, path))
	if e != nil {
		t.Fatal(e)
	}
	if next.Version != Version {
		t.Fatalf("migrated version = %d", next.Version)
	}
	res2, e := Migrate(path, MigrateOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if !res2.Idempotent {
		t.Fatalf("rerun not idempotent: %+v", res2)
	}
}

func TestMigrateInterruptedKeepsOldManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	legacy := []byte(`{"version":1,"media_content_digest":"aaaa","clips":[
		{"id":"c1","event_id":"e1","start_sec":2.0,"end_sec":4.0,"path":"c1.mp4","bytes":10,"sha256":"aa","width":1280,"height":720,"fps":30}
	]}`)
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	_, e := Migrate(path, MigrateOptions{InterruptAfterStage: true})
	if e == nil || e.Code != timebase.CodeInterrupted {
		t.Fatalf("want INTERRUPTED, got %v", e)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(cur) != string(legacy) {
		t.Fatal("interrupted migration replaced the old manifest")
	}
	rollback, state := store.Recover(dir, "manifest.json")
	if !rollback || state == "" {
		t.Fatalf("rollback=%t state=%q", rollback, state)
	}
	res, e := Migrate(path, MigrateOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if !res.Migrated {
		t.Fatalf("resume did not migrate: %+v", res)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
