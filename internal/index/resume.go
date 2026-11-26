package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"example.com/archiveverify/internal/cmaf"
	"example.com/archiveverify/internal/stream"
)

// FingerprintInput hashes the manifest and every referenced media file.
func FingerprintInput(manifestPath string) (string, *cmaf.Manifest, string, error) {
	root := filepath.Dir(manifestPath)
	m, err := cmaf.LoadManifest(manifestPath)
	if err != nil {
		return "", nil, root, err
	}
	h := sha256.New()
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", nil, root, err
	}
	_, _ = h.Write(manifestBytes)
	paths := []string{m.InitPath}
	for _, ev := range m.Events {
		if ev.Path != "" {
			paths = append(paths, ev.Path)
		}
	}
	seen := map[string]bool{}
	for _, rel := range paths {
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", m, root, err
		}
		sum := sha256.Sum256(b)
		_, _ = h.Write([]byte(rel))
		_, _ = h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil)), m, root, nil
}

// SaveIndexAtomic atomically writes an index document.
func SaveIndexAtomic(path string, idx *Index) error {
	return stream.SaveJSONAtomic(path, idx)
}

// LoadIndex reads an index document.
func LoadIndex(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// BuildOrResume uses fp/checkpoint/digest to skip duplicate index writes.
func BuildOrResume(ctx context.Context, manifestPath, indexPath, checkpointPath string, opts BuildOptions) (*BuildResult, bool, error) {
	fp, m, root, err := FingerprintInput(manifestPath)
	if err != nil {
		return nil, false, err
	}
	opts.Root = root
	opts.InputFingerprint = fp

	cp, err := stream.LoadCheckpoint(checkpointPath)
	if err != nil {
		return nil, false, err
	}
	resumed := false
	if cp.MatchesInput(fp) && cp.Completed && cp.Digest != "" {
		if saved, lerr := LoadIndex(indexPath); lerr == nil && DigestIndex(saved) == cp.Digest {
			resumed = true
		}
	}

	res, err := Build(ctx, m, opts)
	if err != nil {
		return res, resumed, err
	}
	res.Index.InputFingerprint = fp
	digest := DigestIndex(res.Index)

	if resumed {
		// Checkpoint and atomically saved index are already current.
		return res, true, nil
	}

	// Persist an incomplete checkpoint first. If index replacement fails,
	// this remains as a resumable stage marker rather than a false completion.
	partial := &stream.Checkpoint{
		InputFingerprint: fp,
		ProcessedEvents:  len(m.Events),
		StageOffsets: map[string]int64{
			"events":        int64(len(m.Events)),
			"index_entries": int64(len(res.Index.Entries)),
		},
		Completed: false,
		IndexPath: indexPath,
	}
	if err := stream.SaveCheckpoint(checkpointPath, partial); err != nil {
		return res, false, err
	}
	if err := SaveIndexAtomic(indexPath, res.Index); err != nil {
		return res, false, err
	}
	complete := *partial
	complete.Completed = true
	complete.Digest = digest
	if err := stream.SaveCheckpoint(checkpointPath, &complete); err != nil {
		return res, false, err
	}
	return res, false, nil
}
