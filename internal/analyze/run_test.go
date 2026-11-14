package analyze_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bbdiff/internal/analyze"
	"bbdiff/internal/fixture"
)

func writeFixtures(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.elf")
	newPath := filepath.Join(dir, "new.elf")
	if err := fixture.Write(oldPath, fixture.Old); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Write(newPath, fixture.New); err != nil {
		t.Fatal(err)
	}
	return oldPath, newPath
}

func runDefault(t *testing.T, oldPath, newPath string) *analyze.Result {
	t.Helper()
	res, err := analyze.Run(oldPath, newPath, analyze.DefaultOptions())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestRunSummary(t *testing.T) {
	oldPath, newPath := writeFixtures(t)
	res := runDefault(t, oldPath, newPath)

	want := analyze.Summary{Added: 1, Removed: 1, Changed: 2, Uncertain: 0}
	if res.Summary != want {
		t.Fatalf("summary = %+v, want %+v", res.Summary, want)
	}
	if len(res.Blocks) != 4 {
		t.Fatalf("got %d blocks, want 4", len(res.Blocks))
	}
	for _, b := range res.Blocks {
		if b.Address == "" || b.Function == "" || b.Kind == "" {
			t.Errorf("block missing a required field: %+v", b)
		}
		if len(b.SHA256) != 64 {
			t.Errorf("block %s sha256 length = %d, want 64", b.Address, len(b.SHA256))
		}
		if b.InstructionCount <= 0 {
			t.Errorf("block %s instruction count = %d", b.Address, b.InstructionCount)
		}
	}
	if len(res.Failures) != 0 {
		t.Fatalf("unexpected failures: %v", res.Failures)
	}
}

func TestRunIsDeterministic(t *testing.T) {
	oldPath, newPath := writeFixtures(t)
	a := runDefault(t, oldPath, newPath)
	b := runDefault(t, oldPath, newPath)
	if a.Summary != b.Summary {
		t.Fatalf("summaries differ: %+v vs %+v", a.Summary, b.Summary)
	}
	if len(a.Blocks) != len(b.Blocks) {
		t.Fatal("block counts differ")
	}
	for i := range a.Blocks {
		if a.Blocks[i].SHA256 != b.Blocks[i].SHA256 || a.Blocks[i].Address != b.Blocks[i].Address {
			t.Fatalf("block %d differs between runs", i)
		}
	}
}

func TestRunWorkersDoNotChangeResult(t *testing.T) {
	oldPath, newPath := writeFixtures(t)
	var results []*analyze.Result
	for _, workers := range []int{1, 2, 4} {
		opt := analyze.DefaultOptions()
		opt.Workers = workers
		res, err := analyze.Run(oldPath, newPath, opt)
		if err != nil {
			t.Fatalf("workers=%d: %v", workers, err)
		}
		results = append(results, res)
	}
	base := results[0]
	for _, res := range results[1:] {
		if res.Summary != base.Summary {
			t.Fatalf("summary differs by worker count: %+v vs %+v", base.Summary, res.Summary)
		}
		if len(res.Blocks) != len(base.Blocks) {
			t.Fatalf("block count differs by worker count")
		}
		for i := range res.Blocks {
			if res.Blocks[i].SHA256 != base.Blocks[i].SHA256 {
				t.Fatalf("block %d hash differs by worker count", i)
			}
		}
	}
}

func TestRunRejectsBadOldImage(t *testing.T) {
	_, newPath := writeFixtures(t)
	bad := filepath.Join(t.TempDir(), "bad.elf")
	if err := os.WriteFile(bad, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := analyze.Run(bad, newPath, analyze.DefaultOptions())
	if err == nil {
		t.Fatal("Run accepted a malformed old image")
	}
	if !strings.Contains(err.Error(), "old image") {
		t.Fatalf("error does not identify the old image: %v", err)
	}
}

func TestRunRejectsBadNewImage(t *testing.T) {
	oldPath, _ := writeFixtures(t)
	bad := filepath.Join(t.TempDir(), "bad.elf")
	if err := os.WriteFile(bad, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := analyze.Run(oldPath, bad, analyze.DefaultOptions())
	if err == nil {
		t.Fatal("Run accepted a malformed new image")
	}
	if !strings.Contains(err.Error(), "new image") {
		t.Fatalf("error does not identify the new image: %v", err)
	}
}

func TestRunCancelledStillReturnsPartialResults(t *testing.T) {
	oldPath, newPath := writeFixtures(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opt := analyze.DefaultOptions()
	opt.Context = ctx
	res, err := analyze.Run(oldPath, newPath, opt)
	if err != nil {
		t.Fatalf("Run with cancelled context: %v", err)
	}
	if !res.Cancelled {
		t.Fatal("result not marked cancelled")
	}
	// The analysis must terminate quickly and not panic.
}

func TestRunTimeout(t *testing.T) {
	oldPath, newPath := writeFixtures(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	opt := analyze.DefaultOptions()
	opt.Context = ctx
	res, err := analyze.Run(oldPath, newPath, opt)
	if err != nil {
		t.Fatalf("Run with expired timeout: %v", err)
	}
	if !res.Cancelled {
		t.Fatal("result not marked cancelled")
	}
}

func TestRunIdenticalImages(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.elf")
	newPath := filepath.Join(dir, "new.elf")
	if err := fixture.Write(oldPath, fixture.Old); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Write(newPath, fixture.Old); err != nil {
		t.Fatal(err)
	}
	res := runDefault(t, oldPath, newPath)
	if res.Summary != (analyze.Summary{}) {
		t.Fatalf("identical images produced %+v", res.Summary)
	}
	if len(res.Blocks) != 0 {
		t.Fatalf("identical images produced %d blocks", len(res.Blocks))
	}
	for _, fn := range res.Functions {
		if fn.Status != "aligned" {
			t.Fatalf("function %q status = %q, want aligned", fn.Name, fn.Status)
		}
	}
}

func TestRunFunctionsReported(t *testing.T) {
	oldPath, newPath := writeFixtures(t)
	res := runDefault(t, oldPath, newPath)
	statuses := make(map[string]string)
	for _, fn := range res.Functions {
		statuses[fn.Name] = fn.Status
	}
	want := map[string]string{
		"init":         "aligned",
		"verify":       "aligned",
		"report_error": "aligned",
		"audit_gate":   "new-only",
		"legacy_path":  "old-only",
	}
	for name, status := range want {
		if statuses[name] != status {
			t.Errorf("function %q status = %q, want %q", name, statuses[name], status)
		}
	}
}

func TestRunCorruptVariantProducesUncertain(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.elf")
	newPath := filepath.Join(dir, "new.elf")
	if err := fixture.Write(oldPath, fixture.Old); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Write(newPath, fixture.Corrupt); err != nil {
		t.Fatal(err)
	}
	res, err := analyze.Run(oldPath, newPath, analyze.DefaultOptions())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Summary.Uncertain == 0 {
		t.Fatalf("corrupt relocation did not produce an uncertain block: %+v", res.Summary)
	}
	var sawDiag bool
	for _, d := range res.Diagnostics {
		if d.Code == "reloc-unresolved" {
			sawDiag = true
		}
	}
	if !sawDiag {
		t.Fatalf("no reloc-unresolved diagnostic: %v", res.Diagnostics)
	}
	for _, b := range res.Blocks {
		if b.Kind != "uncertain" {
			continue
		}
		if b.Confidence >= 100 {
			t.Errorf("uncertain block kept full confidence")
		}
		if len(b.Reasons) == 0 {
			t.Errorf("uncertain block has no explanation")
		}
	}
}
