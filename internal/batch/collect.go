package batch

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// audioExtensions bounds directory discovery to files that can plausibly be
// audio. Explicitly listed files bypass the filter: if the user names a file,
// the probe gets to explain why it is not usable.
var audioExtensions = map[string]bool{
	".wav": true, ".wave": true, ".flac": true, ".aif": true, ".aiff": true,
	".aifc": true, ".aac": true, ".m4a": true, ".mp3": true, ".mp2": true,
	".m4b": true, ".caf": true, ".ogg": true, ".opus": true, ".wma": true,
}

// Collection holds discovered files and discovery errors.
type Collection struct {
	Files []Item
	Errs  []EntryError
}

// Empty reports whether discovery found no files and no errors.
func (c Collection) Empty() bool { return len(c.Files) == 0 && len(c.Errs) == 0 }

// Item is one file to scan with its display path.
type Item struct {
	Path string
	// Display is the path shown in the report: relative to the scan root for
	// directory inputs, as typed for explicit files.
	Display string
}

// EntryError is a discovery-time failure that already has a report entry.
type EntryError struct {
	Display string
	Stage   string
	Err     error
}

// Collect expands the inputs into a de-duplicated, sorted file list. Symlinked
// directories are skipped, and a path reachable twice is scanned once.
func Collect(inputs []string) (Collection, error) {
	var col Collection
	seen := map[string]string{} // canonical path -> display
	addErr := func(display, stage string, err error) {
		col.Errs = append(col.Errs, EntryError{Display: display, Stage: stage, Err: err})
	}

	var walk func(root, dir string)
	addFile := func(root, full string) {
		if root != "" && !audioExtensions[strings.ToLower(filepath.Ext(full))] {
			return
		}
		// De-duplicate by canonical path so the same file reached through a
		// symlink, a relative spelling or two overlapping roots is scanned
		// once. EvalSymlinks fails for a path that raced away; the abs path is
		// still a usable key in that case.
		canon := full
		if resolved, err := filepath.EvalSymlinks(full); err == nil {
			canon = resolved
		}
		if abs, err := filepath.Abs(canon); err == nil {
			canon = abs
		}
		if _, dup := seen[canon]; dup {
			return
		}
		seen[canon] = full
		col.Files = append(col.Files, Item{Path: full, Display: displayPath(root, full)})
	}

	walk = func(root, dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			addErr(displayPath(root, dir), "discover", err)
			return
		}
		// Sort here as well as at the end: error order is then independent of
		// the filesystem's directory order too.
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			full := filepath.Join(dir, e.Name())
			if e.Type()&fs.ModeSymlink != 0 {
				// Follow symlinks only to regular files. Directory symlinks are
				// skipped so a cycle cannot loop and a shared tree is not
				// scanned twice under two names.
				info, err := os.Stat(full)
				if err != nil {
					addErr(displayPath(root, full), "discover", err)
					continue
				}
				if info.Mode().IsRegular() {
					addFile(root, full)
				}
				continue
			}
			if e.IsDir() {
				walk(root, full)
				continue
			}
			info, err := e.Info()
			if err != nil {
				addErr(displayPath(root, full), "discover", err)
				continue
			}
			if !info.Mode().IsRegular() {
				// Sockets, FIFOs and devices would block or never end; a file
				// scan has no business opening them.
				continue
			}
			addFile(root, full)
		}
	}

	for _, raw := range inputs {
		input := filepath.Clean(raw)
		info, err := os.Stat(input) // follows a symlink to a file or directory
		if err != nil {
			addErr(raw, "discover", err)
			continue
		}
		if info.IsDir() {
			walk(input, input)
			continue
		}
		if !info.Mode().IsRegular() {
			addErr(raw, "discover", fmt.Errorf("not a regular file"))
			continue
		}
		// Explicit files keep the path as typed and bypass the extension
		// filter, so the probe can explain non-audio input.
		addFile("", input)
	}

	sort.SliceStable(col.Files, func(i, j int) bool {
		return col.Files[i].Display < col.Files[j].Display
	})
	sort.SliceStable(col.Errs, func(i, j int) bool {
		return col.Errs[i].Display < col.Errs[j].Display
	})
	return col, nil
}

// displayPath renders a discovered path relative to the scan root, with
// forward slashes so reports are comparable across platforms.
func displayPath(root, full string) string {
	if root == "" {
		return filepath.ToSlash(full)
	}
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return filepath.ToSlash(full)
	}
	return filepath.ToSlash(rel)
}
