package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"liveregress/internal/fixture"
)

// TestOutputsStayInsideRoot asserts that a default run never writes outside
// the input root and that no output path escapes it.
func TestOutputsStayInsideRoot(t *testing.T) {
	dir := t.TempDir()
	tree, err := fixture.GenerateOne(dir, fixture.CaseClean)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Run(Options{Root: tree.Root, Case: fixture.CaseClean})
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(tree.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rep.OutRoot, root+string(filepath.Separator)) {
		t.Fatalf("output root %q is outside input root %q", rep.OutRoot, root)
	}
	// No default output may be created beside the root.
	sibling := filepath.Join(filepath.Dir(root), ".out")
	if _, err := os.Stat(sibling); err == nil {
		t.Fatalf("run created %s outside the root", sibling)
	}
}
