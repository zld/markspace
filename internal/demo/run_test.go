package demo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/archive"
	"mediad/internal/model"
)

func TestRunEndToEnd(t *testing.T) {
	dir := t.TempDir()
	rep, err := Run(context.Background(), dir, 17)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.AllExpectationsMet || rep.Summary.ChecksFailed != 0 {
		t.Fatalf("expectations not met: %+v", rep.Summary)
	}
	// 3 success samples + 1 duplicate + 12 corrupt (3 codecs * 4) + 1 restore.
	if rep.Summary.Success != 3 {
		t.Errorf("success=%d want 3", rep.Summary.Success)
	}
	if rep.Summary.Corrupt != 12 {
		t.Errorf("corrupt=%d want 12", rep.Summary.Corrupt)
	}
	if rep.Summary.Restored != 1 || rep.Summary.Duplicates != 1 {
		t.Errorf("restored=%d duplicates=%d", rep.Summary.Restored, rep.Summary.Duplicates)
	}
	// Every corrupt row carries code, exit 2 and a reportable offset.
	for _, r := range rep.Rows {
		if r.ExitCode == model.ExitCorrupt {
			if r.ErrorCode == "" {
				t.Errorf("corrupt row missing error code: %+v", r)
			}
			if r.ErrorOffset < -1 {
				t.Errorf("invalid error offset: %d", r.ErrorOffset)
			}
		}
		if r.ExitCode == model.ExitOK && r.Case == "success" {
			if fails := CheckAcceptance(r); len(fails) != 0 {
				t.Errorf("success row fails acceptance %v: %+v", fails, r)
			}
		}
	}
	// Manifest has no duplicates and is loadable.
	if err := archive.EnsureNoDuplicateEntries(rep.ManifestPath); err != nil {
		t.Fatalf("duplicate manifest entries: %v", err)
	}
}

func TestRunDeterministicAcrossWorkdirs(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	r1, err := Run(context.Background(), d1, 23)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Run(context.Background(), d2, 23)
	if err != nil {
		t.Fatal(err)
	}
	b1, err := os.ReadFile(r1.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(r2.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	// Same seed => byte-identical manifest even across separate work dirs.
	if string(b1) != string(b2) {
		t.Fatal("manifests differ for the same seed")
	}
}

func TestRunIdempotentOnRerun(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(context.Background(), dir, 41); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "archive", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A second run over the same work dir must reuse committed results and
	// leave the manifest byte-identical, exiting successfully.
	rep2, err := Run(context.Background(), dir, 41)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !rep2.AllExpectationsMet {
		t.Fatalf("rerun expectations not met: %+v", rep2.Summary)
	}
	after, err := os.ReadFile(filepath.Join(dir, "archive", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("manifest changed on idempotent rerun")
	}
}
