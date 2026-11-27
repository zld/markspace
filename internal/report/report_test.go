package report

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediapipe/internal/model"
)

func baseSummary() *Summary {
	return &Summary{
		Seed: 17, Versions: 3, Window: 64,
		Metrics: Metrics{
			AVSyncErrorUS: 12_000, LossObserved: 40, LossRecovered: 39,
			LatencyP95US: 8_400, LatencyMaxUS: 11_000,
			Missing: 2, Conflicts: 1, RecoveredIndexes: 1, Packets: 1000,
		},
		Status: Status{State: StateCommitted},
	}
}

func TestDeterministicDigest(t *testing.T) {
	a := baseSummary()
	b := baseSummary()
	// Feed digests in different orders: canonicalisation must erase the
	// difference.
	a.AddDigest("z", []byte("z"))
	a.AddDigest("a", []byte("a"))
	b.AddDigest("a", []byte("a"))
	b.AddDigest("z", []byte("z"))
	da, err := a.ManifestDigest()
	if err != nil {
		t.Fatal(err)
	}
	db, err := b.ManifestDigest()
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Fatalf("digest depends on insertion order: %s vs %s", da, db)
	}
	c := baseSummary()
	c.Metrics.LossRecovered = 38
	dc, _ := c.ManifestDigest()
	if dc == da {
		t.Fatal("different metrics produced the same digest")
	}
}

func TestRollbackClearsUnconfirmedOutput(t *testing.T) {
	s := baseSummary()
	s.AddDigest("media", []byte("payload"))
	err := model.New(model.KindCorrupt, model.StageMP4Scan, "clip.mp4", "scan",
		errors.New("truncated box")).WithVersion("v720p").WithTrack("video").WithOffset(4096)
	s.Rollback(err)
	if s.Status.State != StateRolledBack {
		t.Fatalf("state = %s", s.Status.State)
	}
	if len(s.Digests) != 0 {
		t.Fatal("rolled-back summary must not expose digests")
	}
	if len(s.Status.Records) != 1 {
		t.Fatalf("records = %+v", s.Status.Records)
	}
	r := s.Status.Records[0]
	if r.Asset != "clip.mp4" || r.Stage != "mp4_scan" || r.Offset != 4096 || r.Kind != "corrupt" {
		t.Fatalf("record = %+v", r)
	}
	if r.Version != "v720p" || r.Track != "video" {
		t.Fatalf("record lost version/track: %+v", r)
	}
}

func TestRollbackRecordsAreDeterministic(t *testing.T) {
	mk := func() *Summary {
		s := baseSummary()
		err := model.New(model.KindCorrupt, model.StageIndex, "a.mp4", "index",
			errors.New("bad table")).WithOffset(1024)
		s.Rollback(err)
		d, derr := s.ManifestDigest()
		if derr != nil {
			t.Fatal(derr)
		}
		return &Summary{Status: Status{Records: s.Status.Records}, Metrics: Metrics{}, Digests: []Digest{{Name: "d", Value: d}}}
	}
	first, _ := mk().ManifestDigest()
	second, _ := mk().ManifestDigest()
	if first != second {
		t.Fatalf("rollback record digest differs across runs: %s vs %s", first, second)
	}
}

func TestMetricsRounding(t *testing.T) {
	m := Metrics{AVSyncErrorUS: 12_500, LossObserved: 3, LossRecovered: 2, LatencyP95US: 8_450}
	if m.AVSyncErrorMS() != 13 {
		t.Fatalf("av ms = %d", m.AVSyncErrorMS())
	}
	if got := m.LossRecoveryPPM(); got != 666_667 {
		t.Fatalf("ppm = %d", got)
	}
	if m.LatencyP95MS() != 8.45 {
		t.Fatalf("p95 = %v", m.LatencyP95MS())
	}
	empty := Metrics{}
	if empty.LossRecoveryPPM() != 1_000_000 {
		t.Fatalf("empty recovery = %d", empty.LossRecoveryPPM())
	}
}

func TestStagerCommitAndDiscard(t *testing.T) {
	root := t.TempDir()
	stageDir := filepath.Join(root, "stage")
	outDir := filepath.Join(root, "out")
	st, err := NewStager(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := st.Stage("summary.json", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if digest != ContentDigest([]byte(`{"ok":true}`)) {
		t.Fatal("digest mismatch")
	}
	// Nothing is visible at the destination before commit.
	if _, err := os.Stat(filepath.Join(outDir, "summary.json")); err == nil {
		t.Fatal("staged payload leaked before commit")
	}
	if err := st.Commit(outDir, []string{"summary.json"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "summary.json"))
	if err != nil {
		t.Fatalf("commit did not publish: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("published %q", data)
	}
}

func TestStagerDiscardLeavesNothing(t *testing.T) {
	root := t.TempDir()
	stageDir := filepath.Join(root, "stage")
	st, err := NewStager(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Stage("summary.json", []byte("x")); err != nil {
		t.Fatal(err)
	}
	files, err := st.Files()
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %v err=%v", files, err)
	}
	if err := st.Discard(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stageDir); !os.IsNotExist(err) {
		t.Fatalf("stage dir still present: %v", err)
	}
	outDir := filepath.Join(root, "out")
	if entries, err := os.ReadDir(outDir); err == nil && len(entries) > 0 {
		t.Fatal("discarded run published output")
	}
}

func TestCommitRejectsRolledBackSummary(t *testing.T) {
	root := t.TempDir()
	st, err := NewStager(filepath.Join(root, "stage"))
	if err != nil {
		t.Fatal(err)
	}
	s := baseSummary()
	s.Rollback(errors.New("failed"))
	if _, err := st.CommitResult(s, "summary.json"); err == nil {
		t.Fatal("rolled-back summary must not commit")
	}
}

func TestCommitResultRoundTrip(t *testing.T) {
	root := t.TempDir()
	st, err := NewStager(filepath.Join(root, "stage"))
	if err != nil {
		t.Fatal(err)
	}
	s := baseSummary()
	digest, err := st.CommitResult(s, "summary.json")
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" {
		t.Fatal("empty digest")
	}
	data, err := os.ReadFile(filepath.Join(root, "stage", "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got Summary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("summary not valid JSON: %v", err)
	}
	if got.Metrics.Missing != 2 || got.Status.State != StateCommitted {
		t.Fatalf("round trip = %+v", got)
	}
}
