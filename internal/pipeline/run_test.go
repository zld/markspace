package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/checkpoint"
	"mediasync/internal/ingest"
	"mediasync/internal/media"
	"mediasync/internal/output"
)

func writePipelineFixture(t *testing.T, dir string, records int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cues := make([]ingest.SubtitleCue, 0, records)
	for i := 0; i < records; i++ {
		start := int64(i * 1000)
		ref := start
		cues = append(cues, ingest.SubtitleCue{
			Index: i + 1, StartMS: start, EndMS: start + 700,
			ReferenceStartMS: &ref, Text: "resume test",
		})
	}
	subs := ingest.SubtitleFile{Cues: cues}
	writeJSON(t, filepath.Join(dir, "subs.json"), subs)
	m := ingest.Manifest{
		Schema: "test",
		Tracks: []ingest.TrackSpec{{
			ID: "subs", Kind: ingest.KindSubtitle, Path: "subs.json", TimeScale: 1000,
		}},
		FrameTimesMS: []int64{0, 1000},
	}
	writeJSON(t, filepath.Join(dir, "manifest.json"), m)
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCancelResumeProducesSameRootAsFreshRun(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input")
	pausedOut := filepath.Join(root, "paused")
	freshOut := filepath.Join(root, "fresh")
	writePipelineFixture(t, input, 4)

	_, err := Run(context.Background(), Options{
		InputDir: input, OutDir: pausedOut, BudgetUnits: 100,
		MaxBytes: 1 << 20, CancelAfterUnits: 2,
	})
	var fe *media.Error
	if !errors.As(err, &fe) || media.ExitCode(err) != 130 {
		t.Fatalf("expected cancel exit 130, got %v", err)
	}
	cp, err := checkpoint.Load(pausedOut)
	if err != nil || cp == nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if cp.State != "paused" || cp.LastCommittedTrack != "subs" || cp.LastRecordPosition != 1 {
		t.Fatalf("bad checkpoint: %+v", cp)
	}

	rep, err := Run(context.Background(), Options{
		InputDir: input, OutDir: pausedOut, BudgetUnits: 100,
		MaxBytes: 1 << 20, Resume: true,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if rep.State != "committed" || rep.RootHash == "" {
		t.Fatalf("bad resumed report: %+v", rep)
	}

	fresh, err := Run(context.Background(), Options{
		InputDir: input, OutDir: freshOut, BudgetUnits: 100, MaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("fresh run: %v", err)
	}
	if fresh.RootHash != rep.RootHash {
		t.Fatalf("resume root %s != fresh %s", rep.RootHash, fresh.RootHash)
	}
	cmp, err := output.CompareDirs(pausedOut, freshOut)
	if err != nil || !cmp.Equal {
		t.Fatalf("compare=%+v err=%v", cmp, err)
	}
	if _, _, err := output.Verify(pausedOut); err != nil {
		t.Fatal(err)
	}
}

func TestQualityFailureExitCodeThree(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input")
	out := filepath.Join(root, "out")
	writePipelineFixture(t, input, 2)
	path := filepath.Join(input, "subs.json")
	var sf ingest.SubtitleFile
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatal(err)
	}
	bad := sf.Cues[0].StartMS + 5
	sf.Cues[0].ReferenceStartMS = &bad
	writeJSON(t, path, sf)

	_, err := Run(context.Background(), Options{InputDir: input, OutDir: out, BudgetUnits: 100, MaxBytes: 1 << 20})
	if media.ExitCode(err) != 3 {
		t.Fatalf("exit=%d err=%v", media.ExitCode(err), err)
	}
	if _, err := os.Stat(filepath.Join(out, output.CatalogName)); !os.IsNotExist(err) {
		t.Fatalf("catalog should not exist, stat err=%v", err)
	}
}
