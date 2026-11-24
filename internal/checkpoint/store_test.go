package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediascan/internal/model"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	in := &State{
		ManifestKey:       "abc",
		CommittedSamples:  2048,
		CompletedSegments: 2,
		TotalSegments:     4,
		Segments: []model.SegmentReport{
			{URI: "a.mp4", SampleCount: 1024, GOPMinTicks: 1000, GOPMaxTicks: 2000},
		},
	}
	if err := st.Save(in); err != nil {
		t.Fatal(err)
	}
	res, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "main" || res.RollbackReason != "" {
		t.Fatalf("source=%s reason=%q", res.Source, res.RollbackReason)
	}
	if res.State.CommittedSamples != 2048 || res.State.CompletedSegments != 2 {
		t.Fatalf("bad progress: %+v", res.State)
	}
	if len(res.State.Segments) != 1 || res.State.Segments[0].URI != "a.mp4" {
		t.Fatalf("segments not persisted: %+v", res.State.Segments)
	}
}

func TestLoadMissingIsFresh(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "nope"))
	res, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "fresh" || res.State.CommittedSamples != 0 {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestCorruptMainFallsBackToBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state")
	st, _ := Open(p)
	if err := st.Save(&State{CommittedSamples: 100, CompletedSegments: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(&State{CommittedSamples: 200, CompletedSegments: 2}); err != nil {
		t.Fatal(err)
	}
	// main is now the 200 state; .bak holds the 100 generation.
	if err := os.WriteFile(p, []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "backup" {
		t.Fatalf("source=%s want backup", res.Source)
	}
	if res.State.CommittedSamples != 100 {
		t.Fatalf("backup samples=%d want 100", res.State.CommittedSamples)
	}
	if !strings.Contains(res.RollbackReason, "previous safe boundary") {
		t.Fatalf("reason not recorded: %q", res.RollbackReason)
	}
}

func TestCorruptEverythingRollsToZero(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state")
	st, _ := Open(p)
	if err := st.Save(&State{CommittedSamples: 100}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p+".bak", []byte("also not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "fresh" || res.State.CommittedSamples != 0 {
		t.Fatalf("expected zero boundary, got %+v", res.State)
	}
	if !strings.Contains(res.RollbackReason, "rolled back") {
		t.Fatalf("rollback reason missing: %q", res.RollbackReason)
	}
}

func TestChecksumTamperDetected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state")
	st, _ := Open(p)
	if err := st.Save(&State{CommittedSamples: 5}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	// Flip a digit inside the persisted state without updating checksum.
	modified := strings.Replace(string(b), `"committed_samples": 5`, `"committed_samples": 9`, 1)
	if modified == string(b) {
		t.Fatal("test setup failed: field not found")
	}
	if err := os.WriteFile(p, []byte(modified), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := st.Load()
	if res.Source != "fresh" {
		t.Fatalf("tampered checkpoint must not load, source=%s", res.Source)
	}
}

func TestOpenDirectoryUsesInnerFile(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(st.Path) != "checkpoint.json" {
		t.Fatalf("path=%s", st.Path)
	}
}
