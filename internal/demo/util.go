package demo

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"mediacore/index"
	"mediacore/manifest"
	"mediacore/media"
	"mediacore/pipeline"
)

// indexPath is the standalone, independently verifiable index document.
func indexPath(dir string) string { return filepath.Join(dir, "index.json") }

// cachePath is the digest-bound cache envelope used to decide rebuilds. It is
// kept separate so index.json remains a portable document for verification.
func cachePath(dir string) string { return filepath.Join(dir, "index.cache.json") }

func writeFile(dir, name string, data []byte) error {
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}

func shortDigest(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// reshapeFrame rewrites frame index n's payload with a small deterministic
// change and re-encodes the whole stream so every checksum stays valid. This
// models a camera catching different content, not byte damage, so the scanner
// accepts the new material and only the input digest changes.
func reshapeFrame(stream []byte, recordSize, n int) ([]byte, int64, error) {
	off := n * recordSize
	if off+recordSize > len(stream) {
		return nil, -1, media.New(media.CatInvalidParams, media.StageCLI, "frame %d outside stream", n)
	}
	frames, err := media.ReadAll(bytes.NewReader(stream), media.DefaultLimits())
	if err != nil {
		return nil, -1, err
	}
	if n >= len(frames) {
		return nil, -1, media.New(media.CatInvalidParams, media.StageCLI, "frame %d outside stream", n)
	}
	frames[n].Payload[0] ^= 0x33
	out, err := media.EncodeStream(frames)
	if err != nil {
		return nil, -1, err
	}
	return out, int64(off), nil
}

// publishDamaged writes the damaged frame stream and its partial index into a
// damaged/ subdirectory with a manifest describing only the verified prefix.
// That material is intentionally not "fixed": verifying it reports the damage.
func (r *Runner) publishDamaged(dir string, rep *Report, damaged []byte, idx *index.Index, reason string) {
	sub := filepath.Join(dir, "damaged")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		rep.Notes = append(rep.Notes, "damaged material not published: "+err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(sub, "frames.bin"), damaged, 0o644); err != nil {
		rep.Notes = append(rep.Notes, "damaged frames not written: "+err.Error())
		return
	}
	b, err := idx.Marshal()
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(sub, "index.json"), b, 0o644)
	// Also seed the working cache with the damaged input's index. The repaired
	// rerun must then reject it as stale and rebuild, which is the invalidation
	// the acceptance criteria ask to show.
	_ = index.WriteCache(cachePath(dir), idx.InputDigest, idx)
	// Build a manifest over the verified prefix so verification can recompute
	// its statistics and then fail specifically on index completeness.
	res, err := pipeline.Run(idx, pipeline.NewBufferSource(damaged, media.DefaultLimits()),
		pipeline.DefaultConfig(media.DefaultTimescale))
	if err != nil || len(idx.Entries) == 0 {
		return
	}
	m, err := manifest.Build(idx, res, manifest.Options{TargetKBPS: 900})
	if err != nil {
		return
	}
	mb, err := m.Marshal()
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(sub, "manifest.json"), mb, 0o644)
	rep.Notes = append(rep.Notes,
		fmt.Sprintf("damaged_material=%s reason=%s verified_prefix=%d",
			sub, reason, idx.EntryCount))
}
