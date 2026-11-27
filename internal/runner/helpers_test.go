package runner_test

import (
	"path/filepath"
	"testing"

	"mediasync/internal/index"
	"mediasync/internal/runner"
)

// scanForDigest performs the same cached scan the pipeline uses, exposing
// the input digest needed to construct a stable task key in tests.
func scanForDigest(t *testing.T, media string, params runner.Params) (*index.ScanResult, string, error) {
	t.Helper()
	res, err := index.Scan(media, params.Limits)
	if err != nil {
		return nil, "", err
	}
	dir := t.TempDir()
	digest, err := index.SaveAtomic(filepath.Join(dir, "idx.json"), res)
	if err != nil {
		return nil, "", err
	}
	return res, digest, nil
}
