package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

func parseInt64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

// listRepDirs returns representation directory names under a dataset root,
// sorted for deterministic processing.
func listRepDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var reps []string
	for _, e := range entries {
		if e.IsDir() {
			reps = append(reps, e.Name())
		}
	}
	sort.Strings(reps)
	return reps, nil
}

// listFragments returns sorted fragment file names in a representation dir.
func listFragments(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && filepath.Ext(name) == ".m4s" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// exists reports whether a path is present.
func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// writeText writes a string file via a temp file's rename for crash safety.
func writeText(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-text-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
