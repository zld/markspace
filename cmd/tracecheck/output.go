package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// output centralizes result lines (stdout) and diagnostics (stderr).
type output struct {
	w   io.Writer
	err io.Writer
}

// key emits one "key=value" line to stdout.
func (o *output) key(k string, v any) {
	fmt.Fprintf(o.w, "%s=%v\n", k, v)
}

// section emits a blank-line separated section header.
func (o *output) section(name string) {
	fmt.Fprintf(o.w, "\n## %s\n", name)
}

// diag emits a diagnostic line to stderr.
func (o *output) diag(format string, args ...any) {
	fmt.Fprintf(o.err, format+"\n", args...)
}

// writeFixture writes data under workDir and returns its path. The file is
// rewritten on every run so the demo never depends on stale bytes.
func writeFixture(workDir, name string, data []byte) (string, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", fmt.Errorf("create work dir: %w", err)
	}
	path := filepath.Join(workDir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write fixture %s: %w", path, err)
	}
	return path, nil
}
