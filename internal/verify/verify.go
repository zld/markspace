// Package verify re-checks a completed run against its manifest: every
// segment file must exist, its byte size and SHA-256 must match the recorded
// descriptor, and the manifest's self-describing digest must be valid.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/model"
	"mediad/internal/segment"
)

// Result summarizes a verification pass.
type Result struct {
	SegmentsChecked int
	BytesChecked    int64
	ManifestSHA256  string
}

// Run verifies the manifest at dir/manifest.json and every segment it lists.
// A missing file, size mismatch or digest mismatch returns an error that
// errors.Is matches against model.ErrChecksum, mapped to exit code 1.
func Run(dir string) (*Result, error) {
	manifestPath := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("verify: cannot read manifest: %w", err)
	}
	var manifest segment.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("verify: invalid manifest: %w", err)
	}

	// Recompute the self-describing digest: it must equal the stored value.
	recorded := manifest.ManifestSHA256
	recomputed, err := manifest.SHA256()
	if err != nil {
		return nil, err
	}
	if recorded == "" || recorded != recomputed {
		return nil, fmt.Errorf("%w: manifest self-digest invalid", model.ErrChecksum)
	}

	res := &Result{ManifestSHA256: recorded}
	for _, d := range manifest.Segments {
		name := fmt.Sprintf("%s-seg-%05d.m4s", d.Representation, d.Index)
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("verify: segment %s: %w", name, err)
		}
		if d.ByteSize != 0 && info.Size() != d.ByteSize {
			return nil, fmt.Errorf("%w: %s size %d != recorded %d",
				model.ErrChecksum, name, info.Size(), d.ByteSize)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("verify: segment %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != d.SHA256 {
			return nil, fmt.Errorf("%w: %s digest mismatch", model.ErrChecksum, name)
		}
		res.SegmentsChecked++
		res.BytesChecked += info.Size()
	}
	return res, nil
}
