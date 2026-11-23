// Package manifest writes the multi-stage evidence documents that make a
// forensic run reviewable. Each pipeline stage (parse, transform, render,
// validate) gets its own JSON record; completed stages are retained even when
// a later stage fails. Repeated runs over identical input produce byte-stable
// documents and the same error summary.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"mediaforensics/internal/model"
)

// Stage names used inside the run manifest.
const (
	StageParse     = "parse"
	StageTransform = "transform"
	StageRender    = "render"
	StageValidate  = "validate"
)

// StageRecord captures one pipeline stage's outcome and evidence pointer.
type StageRecord struct {
	Name      string                 `json:"name"`
	Status    string                 `json:"status"` // ok | degraded | failed
	Offset    int64                  `json:"offset"`
	Detail    string                 `json:"detail,omitempty"`
	Artifacts []string               `json:"artifacts,omitempty"`
	Errors    []*model.ForensicError `json:"errors"`
}

// RunManifest is the top-level evidence document for one run.
type RunManifest struct {
	Seed           int64         `json:"seed"`
	Case           string        `json:"case"`
	Stages         []StageRecord `json:"stages"`
	FrameCount     int           `json:"frame_count"`
	GapCount       int           `json:"gap_count"`
	SyncErrorMs    float64       `json:"sync_error_ms"`
	RecoveredRate  float64       `json:"recovered_rate"`
	P95LatencyMs   float64       `json:"p95_latency_ms"`
	ErrorSummary   []string      `json:"error_summary"`
	ExitCode       int           `json:"exit_code"`
	ManifestSHA256 string        `json:"manifest_sha256"`
}

// Writer owns the output directory and enforces the path-safety constraint.
type Writer struct {
	dir string
}

// NewWriter creates the evidence directory, rejecting path traversal and
// non-local destinations.
func NewWriter(dir string) (*Writer, error) {
	if dir == "" {
		return nil, &PathError{Reason: "empty output path"}
	}
	clean := filepath.Clean(dir)
	// Reject any traversal segment; absolute local paths like /tmp/... are
	// allowed because the platform runs fully offline.
	if strings.Contains(clean, "..") {
		return nil, &PathError{Reason: "path traversal not allowed: " + dir}
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return nil, err
	}
	return &Writer{dir: clean}, nil
}

// Dir returns the cleaned output directory.
func (w *Writer) Dir() string { return w.dir }

// PathError marks a rejected output location.
type PathError struct{ Reason string }

func (e *PathError) Error() string { return "manifest: " + e.Reason }

// safeJoin prevents escaping the output directory via crafted artifact names.
func (w *Writer) safeJoin(name string) (string, error) {
	if strings.Contains(name, "..") || filepath.IsAbs(name) {
		return "", &PathError{Reason: "unsafe artifact name: " + name}
	}
	full := filepath.Join(w.dir, filepath.Clean("/"+name))
	if !strings.HasPrefix(full, w.dir+string(os.PathSeparator)) && full != w.dir {
		return "", &PathError{Reason: "artifact escapes output dir: " + name}
	}
	return full, nil
}

// WriteStage serializes one stage record to <dir>/stage-<name>.json using
// indentation that is stable across Go versions and runs.
func (w *Writer) WriteStage(rec StageRecord) (string, error) {
	name := "stage-" + rec.Name + ".json"
	path, err := w.safeJoin(name)
	if err != nil {
		return "", err
	}
	data, err := marshalStable(rec)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// WriteRun writes the final run manifest, then computes its own checksum over
// the checksum-free body and stores it alongside.
func (w *Writer) WriteRun(rm RunManifest) (string, string, error) {
	rm.ManifestSHA256 = ""
	body, err := marshalStable(rm)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(body)
	rm.ManifestSHA256 = hex.EncodeToString(sum[:])
	final, err := marshalStable(rm)
	if err != nil {
		return "", "", err
	}
	path, err := w.safeJoin("run-manifest.json")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, final, 0o644); err != nil {
		return "", "", err
	}
	// A standalone checksum file supports offline verification.
	sumPath, err := w.safeJoin("run-manifest.sha256")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(sumPath, []byte(rm.ManifestSHA256+"  run-manifest.json\n"), 0o644); err != nil {
		return "", "", err
	}
	return path, rm.ManifestSHA256, nil
}

// ErrorDigest builds a deterministic textual summary from forensic errors:
// identical inputs always yield the same ordered summary.
func ErrorDigest(errs []*model.ForensicError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Stage+"|"+e.Phase+"|t"+itoa(e.TrackID)+"|off"+itoa(int(e.Offset)))
	}
	return out
}

// marshalStable centralizes JSON encoding so every artifact shares formatting.
func marshalStable(v interface{}) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
