package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"replay/internal/fault"
)

func entry(path, kind, data string) Entry {
	return Entry{Path: path, Kind: kind, SHA256: HashFile([]byte(data)), Size: int64(len(data))}
}

func TestBuildSortsAndHashes(t *testing.T) {
	doc, err := Build("run-1", "normal", []string{"v1", "v2"},
		[]Entry{entry("b.json", "plan", "b"), entry("a.json", "asset", "a")},
		[]Source{{Path: "b.json", Track: "audio", Version: "v1"}},
		[]StageHash{{Stage: "map", InputHash: "i", OutputHash: "o"}},
		Stats{Tracks: 1, Packets: 10})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if doc.Entries[0].Path != "a.json" || doc.Entries[1].Path != "b.json" {
		t.Fatalf("entries not sorted: %+v", doc.Entries)
	}
	if len(doc.ArtifactHash) != 64 {
		t.Fatalf("hash=%q", doc.ArtifactHash)
	}
}

func TestBuildRejectsBadEntries(t *testing.T) {
	if _, err := Build("r", "c", nil, []Entry{{Path: "", SHA256: HashFile(nil)}}, nil, nil, Stats{}); err == nil {
		t.Fatal("expected error for empty path")
	}
	if _, err := Build("r", "c", nil, []Entry{{Path: "a", SHA256: "deadbeef"}}, nil, nil, Stats{}); err == nil {
		t.Fatal("expected error for short digest")
	}
}

func TestAggregateHashIndependentOfInputOrder(t *testing.T) {
	a := entry("a", "asset", "alpha")
	b := entry("b", "plan", "beta")
	d1, _ := Build("run", "normal", []string{"v1"}, []Entry{a, b}, nil, nil, Stats{Packets: 3})
	d2, _ := Build("run", "normal", []string{"v1"}, []Entry{b, a}, nil, nil, Stats{Packets: 3})
	if d1.ArtifactHash != d2.ArtifactHash {
		t.Fatal("manifest hash depends on entry order")
	}
}

func TestManifestHashChangesWithContent(t *testing.T) {
	a, _ := Build("run", "normal", []string{"v1"}, []Entry{entry("a", "asset", "alpha")}, nil, nil, Stats{})
	b, _ := Build("run", "normal", []string{"v1"}, []Entry{entry("a", "asset", "ALPHA")}, nil, nil, Stats{})
	if a.ArtifactHash == b.ArtifactHash {
		t.Fatal("content change must change the manifest hash")
	}
}

func TestManifestHashChangesWithStats(t *testing.T) {
	e := []Entry{entry("a", "asset", "alpha")}
	a, _ := Build("run", "normal", nil, e, nil, nil, Stats{Conflicts: 0})
	b, _ := Build("run", "normal", nil, e, nil, nil, Stats{Conflicts: 1})
	if a.ArtifactHash == b.ArtifactHash {
		t.Fatal("stats change must change the manifest hash")
	}
}

func TestWriteAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	data := []byte("published")
	path := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := EntryFor(dir, path, "plan")
	if err != nil {
		t.Fatalf("EntryFor: %v", err)
	}
	doc, err := Build("run", "normal", []string{"v1"}, []Entry{e}, nil, nil, Stats{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	out, digest, err := doc.Write(dir)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	loaded, err := Load(out)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if digest != loaded.ArtifactHash {
		t.Fatalf("digest=%s loaded=%s", digest, loaded.ArtifactHash)
	}
	bad, err := Verify(dir, loaded)
	if err != nil {
		t.Fatalf("Verify: %v (%v)", err, bad)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, _ := EntryFor(dir, path, "plan")
	doc, _ := Build("run", "normal", nil, []Entry{e}, nil, nil, Stats{})
	if _, _, err := doc.Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad, err := Verify(dir, doc)
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if len(bad) != 1 {
		t.Fatalf("bad=%v", bad)
	}
	if !fault.IsKind(err, fault.KindManifest) {
		t.Fatalf("kind=%v", err)
	}
}

func TestLoadRejectsTamperedManifest(t *testing.T) {
	dir := t.TempDir()
	e := entry("a", "asset", "alpha")
	doc, _ := Build("run", "normal", nil, []Entry{e}, nil, nil, Stats{})
	out, _, err := doc.Write(dir)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, _ := os.ReadFile(out)
	// Flip the stats in the file without recomputing the digest.
	tampered := string(data)
	tampered = replaceOnce(tampered, `"packets": 0`, `"packets": 9`)
	if err := os.WriteFile(out, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(out); err == nil {
		t.Fatal("expected load to reject tampered manifest")
	}
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

func TestCompareReportsChanges(t *testing.T) {
	base, _ := Build("run", "normal", nil,
		[]Entry{entry("a", "asset", "alpha"), entry("b", "asset", "beta")}, nil, nil, Stats{})
	cur, _ := Build("run", "normal", nil,
		[]Entry{entry("a", "asset", "ALPHA"), entry("c", "asset", "gamma")}, nil, nil, Stats{})

	diff := Compare(base, cur)
	if diff.Same {
		t.Fatal("expected differences")
	}
	if len(diff.ChangedPaths) != 1 || diff.ChangedPaths[0] != "a" {
		t.Fatalf("changed=%v", diff.ChangedPaths)
	}
	if len(diff.AddedPaths) != 1 || diff.AddedPaths[0] != "c" {
		t.Fatalf("added=%v", diff.AddedPaths)
	}
	if len(diff.RemovedPaths) != 1 || diff.RemovedPaths[0] != "b" {
		t.Fatalf("removed=%v", diff.RemovedPaths)
	}
	if diff.Changes() != 3 {
		t.Fatalf("changes=%d", diff.Changes())
	}
}

func TestCompareIdenticalIsSame(t *testing.T) {
	e := []Entry{entry("a", "asset", "alpha")}
	a, _ := Build("run", "normal", []string{"v1"}, e, nil, nil, Stats{Packets: 5})
	b, _ := Build("run", "normal", []string{"v1"}, []Entry{entry("a", "asset", "alpha")}, nil, nil, Stats{Packets: 5})
	diff := Compare(a, b)
	if !diff.Same || diff.Changes() != 0 {
		t.Fatalf("diff=%+v", diff)
	}
}

func TestEntryForRejectsOutsidePath(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)
	if _, err := EntryFor(dir, outside, "asset"); err == nil {
		t.Fatal("expected outside-path rejection")
	}
}

func TestVerifyRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "evil.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)
	doc := &Document{
		SchemaVersion: SchemaVersion,
		Entries:       []Entry{{Path: "../evil.txt", Kind: "asset", SHA256: HashFile([]byte("x")), Size: 1}},
	}
	doc.ArtifactHash, _ = AggregateHash(doc)
	if _, err := Verify(dir, doc); err == nil {
		t.Fatal("expected path escape rejection")
	}
}
