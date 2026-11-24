package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediarc/internal/checkpoint"
	"mediarc/internal/media"
	"mediarc/internal/model"
)

func testOptions(dir string, interrupt int) Options {
	o := DefaultOptions()
	o.Workdir = dir
	o.Seed = 17
	o.Frames = 32
	o.AudioPackets = 128
	o.ChunkSize = 8
	o.InterruptAfter = interrupt
	o.Inject = []string{"bad-sha", "truncated", "late", "duplicate"}
	return o
}

func TestDemoInterruptResumeProducesStableMP4(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "accept")
	o := testOptions(dir, 0)
	if _, err := GenerateDemo(o); err != nil {
		t.Fatal(err)
	}

	o.InterruptAfter = 2
	first, err := Run(o)
	var ee *model.ExitError
	if !errors.As(err, &ee) || ee.Code != model.ExitInterrupted {
		t.Fatalf("first err=%v want exit 10", err)
	}
	if first.Summary.CommittedChunks != 2 {
		t.Fatalf("committed=%d want 2", first.Summary.CommittedChunks)
	}
	store := checkpoint.New(dir)
	for i := 0; i < 2; i++ {
		b, err := os.ReadFile(store.ChunkPath(i))
		if err != nil {
			t.Fatalf("chunk %d mp4: %v", i, err)
		}
		tracks, err := media.ProbeBytes(b)
		if err != nil || len(tracks) != 2 {
			t.Fatalf("chunk %d tracks=%+v err=%v", i, tracks, err)
		}
	}

	o.InterruptAfter = 0
	second, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	if second.Recovery.ResumedFromChunk != 2 {
		t.Fatalf("resumed_from=%d want 2", second.Recovery.ResumedFromChunk)
	}
	if second.Summary.ChunkCount != 4 || second.Summary.OutputSHA256 == "" {
		t.Fatalf("summary=%+v", second.Summary)
	}
	if len(second.Summary.OutputSHA256) != 64 {
		t.Fatalf("sha length=%d", len(second.Summary.OutputSHA256))
	}
	b, err := os.ReadFile(NewPaths(dir).FinalMP4)
	if err != nil {
		t.Fatal(err)
	}
	tracks, err := media.ProbeBytes(b)
	if err != nil || len(tracks) != 2 {
		t.Fatalf("probe tracks=%+v err=%v", tracks, err)
	}

	third, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	if third.Summary.OutputSHA256 != second.Summary.OutputSHA256 {
		t.Fatalf("checksum changed: %s vs %s", third.Summary.OutputSHA256, second.Summary.OutputSHA256)
	}
}

func TestSyncBeyondOneFrameReturnsExit3(t *testing.T) {
	dir := t.TempDir()
	o := testOptions(dir, 0)
	if _, err := GenerateDemo(o); err != nil {
		t.Fatal(err)
	}
	o.AVStartOffsetMS = 50 // > 40 ms frame period
	_, err := Run(o)
	var ee *model.ExitError
	if !errors.As(err, &ee) || ee.Code != model.ExitSyncConfig {
		t.Fatalf("err=%v want exit 3", err)
	}
}

func TestUnknownLayoutReturnsExit3(t *testing.T) {
	dir := t.TempDir()
	o := testOptions(dir, 0)
	o.SourceLayout = "surprise-7.1.4"
	if _, err := GenerateDemo(o); err == nil {
		t.Fatal("demo should reject unknown layout")
	}
	// Generate with valid layout, then override persisted config behavior.
	o.SourceLayout = "stereo"
	if _, err := GenerateDemo(o); err != nil {
		t.Fatal(err)
	}
	o.SourceLayout = "surprise-7.1.4"
	_, err := Run(o)
	var ee *model.ExitError
	if !errors.As(err, &ee) || ee.Code != model.ExitSyncConfig {
		t.Fatalf("err=%v want exit 3", err)
	}
}
