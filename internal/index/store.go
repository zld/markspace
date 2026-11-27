package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"mediasync/internal/model"
)

// envelopeVersion is bumped whenever the on-disk index format changes.
const envelopeVersion = 1

// Envelope wraps a ScanResult with an integrity hash of its canonical JSON
// payload. Load refuses any envelope whose hash or version does not match,
// so a truncated/tampered index always triggers a full rebuild.
type Envelope struct {
	Version        int         `json:"version"`
	PayloadSHA256  string      `json:"payload_sha256"`
	Payload        *ScanResult `json:"payload"`
}

// canonicalPayload renders the deterministic byte sequence that is hashed.
func canonicalPayload(res *ScanResult) ([]byte, error) {
	return json.MarshalIndent(res, "", "  ")
}

// SaveAtomic writes the index to dst via a temp file + fsync + rename, so a
// crash can never leave a partially written index in place.
func SaveAtomic(dst string, res *ScanResult) (string, error) {
	payload, err := canonicalPayload(res)
	if err != nil {
		return "", model.NewError(model.ClassIndexCorrupt, "encode index: %v", err)
	}
	sum := sha256.Sum256(payload)
	env := Envelope{
		Version:       envelopeVersion,
		PayloadSHA256: hex.EncodeToString(sum[:]),
		Payload:       res,
	}
	body, err := json.MarshalIndent(&env, "", "  ")
	if err != nil {
		return "", model.NewError(model.ClassIndexCorrupt, "encode envelope: %v", err)
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", model.NewError(model.ClassIndexCorrupt, "mkdir %s: %v", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return "", model.NewError(model.ClassIndexCorrupt, "create temp index: %v", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := io.Copy(tmp, bytes.NewReader(body)); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", model.NewError(model.ClassIndexCorrupt, "write index: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", model.NewError(model.ClassIndexCorrupt, "fsync index: %v", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", model.NewError(model.ClassIndexCorrupt, "close index: %v", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		cleanup()
		return "", model.NewError(model.ClassIndexCorrupt, "rename index: %v", err)
	}
	return hex.EncodeToString(sum[:]), nil
}

// Load reads and verifies an index envelope. A missing file returns
// (nil, nil) so callers can fall through to a rebuild; every other failure
// is a structured ClassIndexCorrupt error.
func Load(path string) (*ScanResult, string, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", model.NewError(model.ClassIndexCorrupt, "read %s: %v", path, err)
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, "", model.AtError(model.ClassIndexCorrupt, path, 0, 0,
			"index is not valid JSON (likely truncated): %v", err)
	}
	if env.Version != envelopeVersion {
		return nil, "", model.AtError(model.ClassIndexCorrupt, path, 0, 0,
			"index version %d, want %d", env.Version, envelopeVersion)
	}
	if env.Payload == nil {
		return nil, "", model.AtError(model.ClassIndexCorrupt, path, 0, 0, "empty index payload")
	}
	payload, err := canonicalPayload(env.Payload)
	if err != nil {
		return nil, "", model.NewError(model.ClassIndexCorrupt, "re-encode: %v", err)
	}
	sum := sha256.Sum256(payload)
	if got := hex.EncodeToString(sum[:]); got != env.PayloadSHA256 {
		return nil, "", model.AtError(model.ClassIndexCorrupt, path, 0, 0,
			"payload hash mismatch: index=%s recomputed=%s", env.PayloadSHA256, got)
	}
	return env.Payload, env.PayloadSHA256, nil
}

// VerifyInput re-hashes the input file and compares it with the digest stored
// inside the index. A mismatch means the container changed and a rebuild is
// required.
func VerifyInput(res *ScanResult, inputPath string) error {
	f, err := os.Open(inputPath)
	if err != nil {
		return model.NewError(model.ClassCorruptBox, "open %s: %v", inputPath, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return model.NewError(model.ClassCorruptBox, "hash %s: %v", inputPath, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != res.InputSHA256 {
		return model.AtError(model.ClassIndexCorrupt, inputPath, 0, 0,
			"input digest changed: index stores %s, file is %s", res.InputSHA256, got)
	}
	return nil
}

// LoadIfFresh loads indexPath and returns its ScanResult only when the index
// is intact and the underlying input is unchanged; otherwise (nil, "", nil).
func LoadIfFresh(indexPath, inputPath string) (*ScanResult, string, error) {
	res, digest, err := Load(indexPath)
	if err != nil || res == nil {
		return res, digest, err
	}
	if err := VerifyInput(res, inputPath); err != nil {
		return nil, "", nil
	}
	return res, digest, nil
}
