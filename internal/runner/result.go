package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"mediasync/internal/model"
)

// ResultName is the conventional result file inside a task directory.
const ResultName = "result.json"

// resultContent holds every result field except the self digest. It is the
// canonical, hashable payload: because it never contains the digest that
// describes it, hashing it has no self-reference and stays stable across
// retries of the same deterministic task.
type resultContent struct {
	TaskKey        string             `json:"task_key"`
	SyncErrorNS    model.Ticks        `json:"sync_error_ns"`
	P95SyncErrorNS model.Ticks        `json:"p95_sync_error_ns"`
	LossRecovery   float64            `json:"loss_recovery_rate"`
	P95LatencyMS   float64            `json:"p95_latency_ms"`
	Duplicates     int                `json:"duplicates"`
	Missing        int                `json:"missing"`
	Unrecovered    int                `json:"unrecovered_outside_window"`
	InputSHA256    string             `json:"input_sha256"`
	IndexSHA256    string             `json:"index_sha256"`
	Committed      int                `json:"committed"`
	Resumed        bool               `json:"resumed"`
	Errors         []model.MediaError `json:"errors,omitempty"`
}

// document is the on-disk envelope: the canonical content plus its embedded
// content digest, so a reader can verify the result with no external record.
type document struct {
	resultContent
	ResultSHA256 string `json:"result_sha256"`
}

// WriteResult atomically serializes the result and returns the SHA-256 of the
// complete serialized file. Two distinct digests are produced on purpose:
//
//   - The embedded result_sha256 (also set on res) hashes the canonical
//     content only. It is self-describing, independent of the envelope's own
//     bytes, and therefore byte-identical every time the same task is retried.
//   - The returned digest hashes the whole written file, matching what an
//     independent reader — including provenance.AttachResult — computes, which
//     is what makes tampering with any on-disk byte detectable.
func WriteResult(dir string, res *Result) (string, error) {
	content := resultContent{
		TaskKey:        res.TaskKey,
		SyncErrorNS:    res.SyncErrorNS,
		P95SyncErrorNS: res.P95SyncErrorNS,
		LossRecovery:   res.LossRecovery,
		P95LatencyMS:   res.P95LatencyMS,
		Duplicates:     res.Duplicates,
		Missing:        res.Missing,
		Unrecovered:    res.Unrecovered,
		InputSHA256:    res.InputSHA256,
		IndexSHA256:    res.IndexSHA256,
		Committed:      res.Committed,
		Resumed:        res.Resumed,
		Errors:         res.Errors,
	}
	canonical, err := json.MarshalIndent(&content, "", "  ")
	if err != nil {
		return "", model.NewError(model.ClassCheckpoint, "encode result content: %v", err)
	}
	contentSum := sha256.Sum256(canonical)
	contentDigest := hex.EncodeToString(contentSum[:])
	res.ResultSHA256 = contentDigest

	body, err := json.MarshalIndent(&document{resultContent: content, ResultSHA256: contentDigest}, "", "  ")
	if err != nil {
		return "", model.NewError(model.ClassCheckpoint, "encode result envelope: %v", err)
	}
	fileSum := sha256.Sum256(body)
	fileDigest := hex.EncodeToString(fileSum[:])

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", model.NewError(model.ClassCheckpoint, "mkdir: %v", err)
	}
	dst := filepath.Join(dir, ResultName)
	tmp, err := os.CreateTemp(dir, ".result-*.tmp")
	if err != nil {
		return "", model.NewError(model.ClassCheckpoint, "temp: %v", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", model.NewError(model.ClassCheckpoint, "write: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", model.NewError(model.ClassCheckpoint, "fsync: %v", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", model.NewError(model.ClassCheckpoint, "close: %v", err)
	}
	if err := os.Rename(name, dst); err != nil {
		_ = os.Remove(name)
		return "", model.NewError(model.ClassCheckpoint, "rename: %v", err)
	}
	return fileDigest, nil
}

// VerifyResult re-hashes the on-disk result's canonical content and compares it
// against the embedded result_sha256, independent of the manifest. It decodes
// the envelope rather than hashing raw bytes, so it validates the content
// digest specifically; the manifest separately guards the whole file's bytes.
func VerifyResult(path, wantDigest string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return model.NewError(model.ClassIndexCorrupt, "read result: %v", err)
	}
	var doc document
	if err := json.Unmarshal(body, &doc); err != nil {
		return model.NewError(model.ClassIndexCorrupt, "decode result: %v", err)
	}
	canonical, err := json.MarshalIndent(&doc.resultContent, "", "  ")
	if err != nil {
		return model.NewError(model.ClassIndexCorrupt, "re-encode result: %v", err)
	}
	sum := sha256.Sum256(canonical)
	if got := hex.EncodeToString(sum[:]); got != wantDigest {
		return model.NewError(model.ClassIndexCorrupt,
			"result content hash mismatch: want %s got %s", wantDigest, got)
	}
	return nil
}
