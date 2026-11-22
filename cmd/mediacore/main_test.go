package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHelpExitsZero(t *testing.T) {
	if code := run([]string{"help"}); code != 0 {
		t.Fatalf("help exit=%d", code)
	}
}

func TestNoArgsIsParameterError(t *testing.T) {
	if code := run(nil); code != 2 {
		t.Fatalf("no args exit=%d", code)
	}
}

func TestUnknownCommandIsParameterError(t *testing.T) {
	if code := run([]string{"frobnicate"}); code != 2 {
		t.Fatalf("unknown command exit=%d", code)
	}
}

func TestDemoMissingOutIsParameterError(t *testing.T) {
	if code := run([]string{"demo"}); code != 2 {
		t.Fatalf("missing --out exit=%d", code)
	}
}

func TestVerifyMissingFlagsIsParameterError(t *testing.T) {
	if code := run([]string{"verify", "--input", "x"}); code != 2 {
		t.Fatalf("missing flags exit=%d", code)
	}
}

func TestDemoThenVerifyEndToEnd(t *testing.T) {
	dir := t.TempDir()
	if code := run([]string{"demo", "--out", dir, "--frames", "240"}); code != 0 {
		t.Fatalf("demo exit=%d", code)
	}
	code := run([]string{
		"verify",
		"--input", filepath.Join(dir, "frames.bin"),
		"--index", filepath.Join(dir, "index.json"),
		"--manifest", filepath.Join(dir, "manifest.json"),
	})
	if code != 0 {
		t.Fatalf("verify exit=%d", code)
	}
}

func TestVerifyRejectsMissingFileWithExit2(t *testing.T) {
	dir := t.TempDir()
	code := run([]string{
		"verify",
		"--input", filepath.Join(dir, "absent.bin"),
		"--index", filepath.Join(dir, "absent.json"),
		"--manifest", filepath.Join(dir, "absent-m.json"),
	})
	if code != 2 {
		t.Fatalf("verify missing files exit=%d", code)
	}
}

func TestVerifyRejectsDamagedMaterialWithExit1(t *testing.T) {
	dir := t.TempDir()
	if code := run([]string{"demo", "--case", "corrupt", "--out", dir, "--frames", "240"}); code != 0 {
		t.Fatalf("corrupt demo exit=%d", code)
	}
	damaged := filepath.Join(dir, "damaged")
	code := run([]string{
		"verify",
		"--input", filepath.Join(damaged, "frames.bin"),
		"--index", filepath.Join(damaged, "index.json"),
		"--manifest", filepath.Join(damaged, "manifest.json"),
	})
	if code != 1 {
		t.Fatalf("verify damaged exit=%d want 1", code)
	}
}

func TestVerifyPathEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	code := run([]string{
		"verify",
		"--input", filepath.Join(dir, "..", "escape.bin"),
		"--index", filepath.Join(dir, "index.json"),
		"--manifest", filepath.Join(dir, "manifest.json"),
	})
	if code != 2 {
		t.Fatalf("path escape exit=%d", code)
	}
}

func TestOutputStaysInsideOutDir(t *testing.T) {
	dir := t.TempDir()
	if code := run([]string{"demo", "--out", filepath.Join(dir, "nested"), "--frames", "200"}); code != 0 {
		t.Fatalf("demo exit=%d", code)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no outputs written")
	}
}
