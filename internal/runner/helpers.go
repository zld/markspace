package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"mediapack/internal/manifest"
	"mediapack/internal/model"
)

func nowMS() int64 { return time.Now().UnixNano() / 1_000_000 }

func limitError(input string, what string, got, limit int64) error {
	var es model.Errors
	es.Newf(model.StageRunner, model.CodeLimitExceeded, input, 0,
		model.ByteRange{Offset: 0}, "%s=%d exceeds configured limit %d", what, got, limit)
	return es.Err()
}

func discardCheckpoint(dir string) error {
	if err := os.Remove(checkpointPath(dir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(checksumSibling(checkpointPath(dir))); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// rollbackToSafePrefix deletes segment outputs beyond the entries that can be
// verified against their stored digest and returns the count safe to keep.
func rollbackToSafePrefix(cp *Checkpoint, segDir string) int {
	keep := 0
	for i, seg := range cp.Completed {
		sum, size, err := hashFileBounded(seg.Path)
		if err != nil || sum != seg.SHA256 || size != seg.Size {
			// This and every later segment are untrusted: remove them.
			for j := i; j < len(cp.Completed); j++ {
				_ = os.Remove(cp.Completed[j].Path)
			}
			return keep
		}
		keep = i + 1
	}
	return keep
}

// writeManifest serializes the plan deterministically to a temp file, verifies
// and atomically replaces the prior manifest.
func writeManifest(dir string, plan *manifest.Plan) (string, error) {
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return "", err
	}
	final := filepath.Join(dir, "manifest.json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", err
	}
	sum, _, err := hashFileBounded(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return sum, nil
}

// combineOutputs streams every segment in index order into one output digest
// using a fixed buffer, so memory stays bounded regardless of total size.
func combineOutputs(dir string, segs []SegmentResult) (string, error) {
	final := filepath.Join(dir, "output.bin")
	tmp := final + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	mw := io.MultiWriter(out, h)
	buf := make([]byte, 64*1024)
	for _, seg := range segs {
		in, oerr := os.Open(seg.Path)
		if oerr != nil {
			out.Close()
			_ = os.Remove(tmp)
			return "", oerr
		}
		if _, cerr := io.CopyBuffer(mw, in, buf); cerr != nil {
			in.Close()
			out.Close()
			_ = os.Remove(tmp)
			return "", cerr
		}
		in.Close()
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if verify, _, err := hashFileBounded(tmp); err != nil || verify != sum {
		_ = os.Remove(tmp)
		return "", cpError(tmp, "combined output verification failed")
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return sum, nil
}
