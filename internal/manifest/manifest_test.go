package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/model"
)

func anomalies() []model.Anomaly {
	return []model.Anomaly{
		{ID: "a2", Type: model.AnomalyChecksum, TrackID: 2, StartNanos: 3_000_000_000,
			EndNanos: 4_000_000_000, SourceOffset: 300, SourceLength: 50,
			Checksum: "abc", ExpectedHash: "def", Recovery: model.StateRecovered},
		{ID: "a1", Type: model.AnomalyTimeWrap, TrackID: 1, StartNanos: 1_000_000_000,
			EndNanos: 1_500_000_000, SourceOffset: 100, SourceLength: 20,
			Recovery: model.StateOpen},
		{ID: "a3", Type: model.AnomalyGap, TrackID: 1, StartNanos: 8_000_000_000,
			EndNanos: 9_000_000_000, SourceOffset: 800, Recovery: model.StateUnrecoverable},
	}
}

func TestWindowFiltersAndSorts(t *testing.T) {
	m := Window("src-h", 0, 5_000_000_000, anomalies())
	if m.AnomalyCount != 2 {
		t.Fatalf("count = %d, want 2 in [0,5s]", m.AnomalyCount)
	}
	// Stable order: track 1 first (a1), then track 2 (a2).
	if m.Anomalies[0].ID != "a1" || m.Anomalies[1].ID != "a2" {
		t.Fatalf("wrong stable order: %s %s", m.Anomalies[0].ID, m.Anomalies[1].ID)
	}
	// Full fields survive into the manifest.
	a1 := m.Anomalies[0]
	if a1.Type != model.AnomalyTimeWrap || a1.SourceOffset != 100 ||
		a1.StartNanos != 1_000_000_000 || a1.Recovery != model.StateOpen {
		t.Fatalf("a1 lost fields: %+v", a1)
	}
}

func TestWindowBoundaryIntersection(t *testing.T) {
	// A range ending exactly at `from` is excluded; one starting exactly at
	// `to` is excluded; one touching either bound is included.
	m := Window("h", 2_000_000_000, 3_000_000_000, anomalies())
	ids := map[string]bool{}
	for _, a := range m.Anomalies {
		ids[a.ID] = true
	}
	// a1 [1s,1.5s] excluded; a2 [3s,4s] touches from=3s? a2 starts at 3s > to=3s.
	// Intersection rule end>=from && start<=to: a2 start=3s <= to=3s => included.
	if !ids["a2"] {
		t.Fatal("a2 touching the to-boundary must be included")
	}
	if ids["a1"] {
		t.Fatal("a1 fully before the window must be excluded")
	}
}

func TestManifestHashDeterministic(t *testing.T) {
	m1 := Window("h", 0, 10_000_000_000, anomalies())
	m2 := Window("h", 0, 10_000_000_000, anomalies())
	h1, err := m1.Hash()
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := m2.Hash()
	if h1 != h2 {
		t.Fatal("identical manifests must hash equal")
	}
	// A different window changes the hash.
	m3 := Window("h", 0, 1_000_000_000, anomalies())
	h3, _ := m3.Hash()
	if h3 == h1 {
		t.Fatal("different window must change the manifest hash")
	}
}

func TestManifestWriteContentNamed(t *testing.T) {
	root := t.TempDir()
	m := Window("h", 0, 10_000_000_000, anomalies())
	path, hash, err := Write(root, m)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != hash+".json" {
		t.Fatalf("manifest must be content-named: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("manifest file must end with newline")
	}
	latest := filepath.Join(root, "manifests", "latest.json")
	if _, err := os.Stat(latest); err != nil {
		t.Fatalf("latest pointer missing: %v", err)
	}
	// Re-writing identical content lands on the same path (idempotent).
	path2, hash2, _ := Write(root, m)
	if path != path2 || hash != hash2 {
		t.Fatal("repeated write must be idempotent")
	}
}

func TestWindowReversedBoundsNormalized(t *testing.T) {
	m := Window("h", 5_000_000_000, 0, anomalies())
	if m.FromNanos != 0 || m.ToNanos != 5_000_000_000 {
		t.Fatalf("reversed bounds must normalize: %+v", m)
	}
	if m.AnomalyCount != 2 {
		t.Fatalf("normalized window count = %d", m.AnomalyCount)
	}
}
