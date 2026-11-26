package main

import (
	"fmt"
	"path/filepath"

	"replaykit/media/audit"
)

// finish finalizes a report, persists <root>/<name>/manifest.json and builds
// the common scenario result. The manifest reference is stored relative to
// root so proof.json stays deterministic across output locations.
func finish(name, root string, expected, code int, rep *audit.Report, ok bool, line string) scenarioResult {
	rep.Finalize()
	dir := filepath.Join(root, name)
	if _, err := rep.WriteManifest(dir); err != nil {
		ok = false
		line += fmt.Sprintf(" manifest-error=%v", err)
	}
	return scenarioResult{
		Name: name, ExpectedCode: expected, Code: code, OK: ok, Line: line,
		Metrics: rep.Metrics, Manifest: filepath.Join(name, "manifest.json"), Rep: rep,
	}
}

func boolOK(b bool) string {
	if b {
		return "OK"
	}
	return "FAIL"
}
