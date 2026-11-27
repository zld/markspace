package migrate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStageOrder(t *testing.T) {
	want := []string{"prepare", "write-temp", "verify", "atomic-replace", "done"}
	got := StageOrder()
	if len(got) != len(want) {
		t.Fatalf("order = %v", got)
	}
	for i := range want {
		if got[i] != want[i] || !IsKnownStage(got[i]) {
			t.Fatalf("stage %d = %q", i, got[i])
		}
	}
	if IsKnownStage("bogus") {
		t.Fatal("bogus stage accepted")
	}
}

func TestLoadPointMissing(t *testing.T) {
	p, err := LoadPoint(t.TempDir())
	if err != nil || p != nil {
		t.Fatalf("point=%v err=%v", p, err)
	}
}

func TestLoadPointCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, pointName), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPoint(dir); err == nil {
		t.Fatal("corrupt point must fail")
	}
}

func TestSavePointRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &Point{Stage: StageWrite, SourceDigest: "abc", Entries: 3}
	if err := savePoint(filepath.Join(dir, pointName), want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != want.Stage || got.Entries != want.Entries || got.SourceDigest != want.SourceDigest {
		t.Fatalf("point = %+v", got)
	}
}

func TestCleanupRemovesArtefacts(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{pointName, tempName} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Cleanup(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{pointName, tempName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s still present", name)
		}
	}
}
