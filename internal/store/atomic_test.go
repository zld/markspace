package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"hlscheck/internal/store"
)

func TestCreateClosePublishes(t *testing.T) {
	dir := t.TempDir()
	f, err := store.Create(dir, "index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("#EXTM3U\n")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#EXTM3U\n" {
		t.Errorf("content = %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.m3u8.tmp")); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
}

func TestAbortLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	f, err := store.Create(dir, "seg.ts")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("partial"))
	f.Abort()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dir not empty after abort: %v", entries)
	}
}

func TestCleanTemps(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.tmp", "b.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CleanTemps(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "keep.ts" {
		t.Errorf("leftover entries: %v", entries)
	}
	if err := store.CleanTemps(filepath.Join(dir, "missing")); err != nil {
		t.Errorf("missing dir should be tolerated: %v", err)
	}
}
