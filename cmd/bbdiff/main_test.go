package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bbdiff/internal/fixture"
)

// runCLI invokes run() with captured streams.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return out.String(), errb.String(), code
}

func TestGenerateFixturesJSON(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, ".work", "old.elf")
	newPath := filepath.Join(dir, ".work", "new.elf")

	stdout, stderr, code := runCLI(t, "--generate-fixtures", "--old", oldPath, "--new", newPath, "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if strings.Contains(stderr, "level=error") {
		t.Fatalf("stderr reports an error: %s", stderr)
	}

	var doc struct {
		Summary struct {
			Added     int `json:"added"`
			Removed   int `json:"removed"`
			Changed   int `json:"changed"`
			Uncertain int `json:"uncertain"`
		} `json:"summary"`
		Blocks []struct {
			Address          string `json:"address"`
			InstructionCount int    `json:"instruction_count"`
			SHA256           string `json:"sha256"`
			Function         string `json:"function"`
			Kind             string `json:"kind"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if doc.Summary.Added != 1 || doc.Summary.Removed != 1 || doc.Summary.Changed != 2 || doc.Summary.Uncertain != 0 {
		t.Fatalf("summary = %+v, want {1 1 2 0}", doc.Summary)
	}
	if len(doc.Blocks) != 4 {
		t.Fatalf("got %d blocks, want 4", len(doc.Blocks))
	}
	for _, b := range doc.Blocks {
		if b.Address == "" || b.Function == "" || b.Kind == "" {
			t.Errorf("block missing required field: %+v", b)
		}
		if len(b.SHA256) != 64 {
			t.Errorf("sha256 = %q, want 64 hex chars", b.SHA256)
		}
		if b.InstructionCount <= 0 {
			t.Errorf("instruction_count = %d", b.InstructionCount)
		}
	}
	// Fixtures must exist and match the deterministic build.
	img, err := fixture.Build(fixture.Old)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, img) {
		t.Fatal("generated old fixture does not match fixture.Build")
	}
}

func TestGenerateFixturesIsReproducible(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.elf")
	newPath := filepath.Join(dir, "new.elf")

	first, _, code := runCLI(t, "--generate-fixtures", "--old", oldPath, "--new", newPath, "--json")
	if code != 0 {
		t.Fatalf("first run exit code = %d", code)
	}
	second, _, code := runCLI(t, "--generate-fixtures", "--old", oldPath, "--new", newPath, "--json")
	if code != 0 {
		t.Fatalf("second run exit code = %d", code)
	}
	if first != second {
		t.Fatal("two runs produced different JSON, so the demo does not reproduce")
	}
}

func TestMissingFlags(t *testing.T) {
	_, stderr, code := runCLI(t)
	if code != exportUsage {
		t.Fatalf("exit code = %d, want %d", code, exportUsage)
	}
	if !strings.Contains(stderr, "--old and --new are required") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestInvalidOldImageExitCode(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.elf")
	if err := os.WriteFile(bad, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, "new.elf")
	if err := fixture.Write(newPath, fixture.New); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, "--old", bad, "--new", newPath)
	if code != exportInvalid {
		t.Fatalf("exit code = %d, want %d (invalid input); stderr = %s", code, exportInvalid, stderr)
	}
}

func TestUnknownFlagExitCode(t *testing.T) {
	_, _, code := runCLI(t, "--not-a-flag")
	if code != exportUsage {
		t.Fatalf("exit code = %d, want %d", code, exportUsage)
	}
}

func TestPositionalArgumentRejected(t *testing.T) {
	_, stderr, code := runCLI(t, "extra")
	if code != exportUsage {
		t.Fatalf("exit code = %d, want %d", code, exportUsage)
	}
	if !strings.Contains(stderr, "positional") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestTraversalOutputPathRejected(t *testing.T) {
	for _, args := range [][]string{
		{"--generate-fixtures", "--old", "../escape.elf", "--new", "ok.elf"},
		{"--generate-fixtures", "--old", "ok.elf", "--new", "../escape.elf"},
	} {
		_, stderr, code := runCLI(t, args...)
		if code != exportUsage {
			t.Fatalf("args %v: exit code = %d, want %d; stderr = %s", args, code, exportUsage, stderr)
		}
		if !strings.Contains(stderr, "traversal") {
			t.Fatalf("args %v: stderr = %q", args, stderr)
		}
	}
}

func TestSameOldAndNewRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.elf")
	_, _, code := runCLI(t, "--generate-fixtures", "--old", path, "--new", path)
	if code != exportUsage {
		t.Fatalf("exit code = %d, want %d", code, exportUsage)
	}
}

func TestCleanRemovesFixtures(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.elf")
	newPath := filepath.Join(dir, "new.elf")
	_, stderr, code := runCLI(t, "--generate-fixtures", "--clean", "--old", oldPath, "--new", newPath, "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	for _, p := range []string{oldPath, newPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after --clean", p)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	stdout, _, code := runCLI(t, "--version")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout, "Capstone 5") {
		t.Fatalf("version output = %q", stdout)
	}
}

func TestTextReport(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.elf")
	newPath := filepath.Join(dir, "new.elf")
	if err := fixture.Write(oldPath, fixture.Old); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Write(newPath, fixture.New); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCLI(t, "--old", oldPath, "--new", newPath)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	for _, want := range []string{"summary:", "added=1", "removed=1", "changed=2", "uncertain=0", "verify", "legacy_path", "audit_gate"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("text report missing %q:\n%s", want, stdout)
		}
	}
}

func TestJSONAndQuiet(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.elf")
	newPath := filepath.Join(dir, "new.elf")
	if err := fixture.Write(oldPath, fixture.Old); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Write(newPath, fixture.New); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCLI(t, "--old", oldPath, "--new", newPath, "--json", "--quiet")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is not valid JSON:\n%s", stdout)
	}
}

func TestCorruptVariantFlag(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.elf")
	newPath := filepath.Join(dir, "new.elf")
	stdout, stderr, code := runCLI(t, "--generate-fixtures", "--new-variant", "corrupt", "--old", oldPath, "--new", newPath, "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	var doc struct {
		Summary struct {
			Uncertain int `json:"uncertain"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Summary.Uncertain != 1 {
		t.Fatalf("uncertain = %d, want 1 for the corrupt variant", doc.Summary.Uncertain)
	}
}

func TestUnknownVariantFlag(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := runCLI(t, "--generate-fixtures", "--new-variant", "bogus", "--old", filepath.Join(dir, "a.elf"), "--new", filepath.Join(dir, "b.elf"))
	if code != exportUsage {
		t.Fatalf("exit code = %d, want %d; stderr = %s", code, exportUsage, stderr)
	}
}
