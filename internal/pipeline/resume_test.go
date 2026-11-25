package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"media-audit/internal/samplegen"
	"media-audit/internal/task"
)

func TestProcessResumesAfterCorruptManifestCheckpoint(t *testing.T) {
	fixtures := t.TempDir()
	state := t.TempDir()
	bundle, err := samplegen.Generate(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	var sample *samplegen.Sample
	for _, s := range bundle.Samples {
		if s.ID == "normal-mxf" {
			sample = s
		}
	}
	if sample == nil {
		t.Fatal("normal-mxf sample missing")
	}

	p1, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	first, err := p1.ProcessSample(sample)
	if err != nil || first.ErrorCount != 0 {
		t.Fatalf("first process: item=%+v err=%v", first, err)
	}
	if err := p1.Close(); err != nil {
		t.Fatal(err)
	}

	key := sample.StableKey
	if err := os.Remove(filepath.Join(state, key+".done.ckpt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, key+".manifest.ckpt"), []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	p2, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p2.ProcessSample(sample)
	if err != nil {
		t.Fatalf("resume process: %v", err)
	}
	if second.SHA256 != first.SHA256 || second.ErrorCount != 0 {
		t.Fatalf("resume changed result: sha %s/%s errors %d/%d",
			first.SHA256, second.SHA256, first.ErrorCount, second.ErrorCount)
	}
	if second.Recovery == nil || len(second.Recovery.CorruptCheckpoints) == 0 ||
		second.Recovery.CorruptCheckpoints[0] != task.StageManifest {
		t.Fatalf("corrupt manifest fallback not recorded: %+v", second.Recovery)
	}
}
