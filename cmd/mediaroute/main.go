// Command mediaroute is the terminal entry point for the media routing
// acceptance flow. It only drives the reusable library packages; no media
// processing logic lives here.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mediaroute/internal/model"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		var te *model.TaskError
		if errors.As(err, &te) {
			fmt.Fprintf(os.Stderr, "error: %s\n", te.Error())
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(exitCode(err))
	}
}

// exitCode maps categories to stable shell exit codes. Unrecoverable inputs
// and manifest/index mismatches must be nonzero.
func exitCode(err error) int {
	te, ok := model.AsTaskError(err)
	if !ok {
		return 1
	}
	switch te.Category {
	case model.CategoryInvalidConfig, model.CategoryPathTraversal:
		return 2
	case model.CategoryUnrecoverable, model.CategoryMissingInit,
		model.CategoryZeroTimescale, model.CategoryCorruptIndex,
		model.CategoryIndexMismatch, model.CategoryManifestMismatch:
		return 3
	default:
		return 1
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stderr)
		return model.NewTaskError(model.CategoryInvalidConfig, "cli", "a subcommand is required")
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return nil
	default:
		usage(stderr)
		return model.NewTaskError(model.CategoryInvalidConfig, "cli",
			fmt.Sprintf("unknown subcommand %q", args[0]))
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `mediaroute - CMAF/fMP4 timeline repair and index rebuild

Usage:
  mediaroute demo   --seed N [--queue N] [--target-bps N] [--out-dir DIR]
  mediaroute verify --input FILE --manifest FILE

demo generates synthetic inputs (clean, sequence gap, tfdt regression,
truncated box, corrupt index), repairs each and prints one JSON record per
case. verify re-checks a delivered file against its manifest.
`)
}

// safeOutputPath joins dir and name and rejects anything that escapes dir,
// including absolute names, "..", and separators inside name.
func safeOutputPath(dir, name string) (string, error) {
	if name == "" {
		return "", model.NewTaskError(model.CategoryInvalidConfig, "cli", "output name is empty")
	}
	// The directory itself must be clean: a ".." component would let a caller
	// write outside the workspace even though the joined name is sanitised.
	// Any parent-directory component is rejected outright, whether it appears
	// at the start or in the middle (e.g. "a/../..") of the supplied path.
	for _, part := range strings.FieldsFunc(dir, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", model.NewTaskError(model.CategoryPathTraversal, "cli",
				fmt.Sprintf("output directory %q contains a parent-directory reference", dir))
		}
	}
	if filepath.IsAbs(dir) {
		// Absolute directories are allowed only if the caller accepts that the
		// name is still confined to that directory; the name checks below keep
		// traversal out. Nothing further is required here.
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", model.NewTaskError(model.CategoryPathTraversal, "cli",
			fmt.Sprintf("output name %q contains a path separator or traversal", name))
	}
	if filepath.IsAbs(name) {
		return "", model.NewTaskError(model.CategoryPathTraversal, "cli",
			fmt.Sprintf("output name %q is absolute", name))
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", model.WrapTaskError(model.CategoryInvalidConfig, "cli", "resolve output dir", err)
	}
	full := filepath.Join(absDir, name)
	rel, err := filepath.Rel(absDir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", model.NewTaskError(model.CategoryPathTraversal, "cli",
			fmt.Sprintf("output %q escapes %q", name, dir))
	}
	return full, nil
}

// writeFileAtomic writes to a temp file in the destination directory and
// renames into place, so a partial run never leaves a truncated artifact.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return model.WrapTaskError(model.CategoryIO, "cli", "create output dir", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return model.WrapTaskError(model.CategoryIO, "cli", "create temp output", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return model.WrapTaskError(model.CategoryIO, "cli", "write temp output", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return model.WrapTaskError(model.CategoryIO, "cli", "sync temp output", err)
	}
	if err := tmp.Close(); err != nil {
		return model.WrapTaskError(model.CategoryIO, "cli", "close temp output", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return model.WrapTaskError(model.CategoryIO, "cli", "rename output", err)
	}
	return nil
}
