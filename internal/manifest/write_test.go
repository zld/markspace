package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"media.rebuild/internal/model"
)

func sample() *Manifest {
	return &Manifest{
		Task: "rebuild", Generation: 1, Committed: true,
		Artifacts: []Artifact{{Name: "b.png", File: "artifacts/rebuild/b.png"}, {Name: "a.png", File: "artifacts/rebuild/a.png"}},
		Summary: &Summary{
			ItemsTotal: 4, ItemsOK: 3, ItemsError: 1,
			MaxSkewMs: 90, ThresholdMs: 40, OverBudget: 1, AnomalyRanges: 1,
			Frames: 3, FrameDigest: "abc",
			RTPDuplicate: 2, RTPGap: 1, MP4Invalid: 1,
		},
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	m := sample()
	if err := Write(path, m); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Task != "rebuild" || got.Summary.ItemsTotal != 4 || got.Summary.FrameDigest != "abc" {
		t.Fatalf("round trip %+v", got)
	}
	if got.Summary.ThresholdMs != 40 {
		t.Fatalf("threshold %d", got.Summary.ThresholdMs)
	}
}

func TestWriteIsAtomicAndStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := Write(path, sample()); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(path)
	if err := Write(path, sample()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(a) != string(b) {
		t.Fatal("encoding not stable")
	}
	if strings.HasSuffix(string(a), ".tmp") {
		t.Fatal("temporary name leaked")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestReadRejectsBadVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"task":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); !model.IsCode(err, model.CodeTxnConflict) {
		t.Fatalf("version accepted: %v", err)
	}
}

func TestReadRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); !model.IsCode(err, model.CodeJournalCorrupt) {
		t.Fatalf("garbage accepted: %v", err)
	}
}
