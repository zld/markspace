package pipeline

import (
	"io"
	"os"
	"path/filepath"

	"continuity/internal/clock"
	"continuity/internal/index"
	"continuity/internal/manifest"
)

// stageWrite copies each verified segment into the content-addressed store
// under OutputDir/store, then commits it to the journal. A checkpoint is
// persisted every cfg.Chunk segments.
func (r *Runner) stageWrite() error {
	storeDir := filepath.Join(r.cfg.OutputDir, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return clock.New(clock.KindIO, "write", "mkdir store: %v", err)
	}

	// On resume, reconcile orphaned content files that were written but
	// never committed (the crash-after-write window).
	if r.cfg.Resume {
		if err := r.reconcileOrphans(storeDir); err != nil {
			return err
		}
	}

	processed := 0
	for _, item := range r.verified {
		src, err := manifest.SafePath(r.manifestDir(item.seg), item.seg.URL)
		if err != nil {
			return err
		}
		data, err := r.readBounded(src)
		if err != nil {
			return err
		}
		sha := index.HashBytes(data)
		rel := index.StorePath(sha, item.repID, item.seg.Index)
		full := filepath.Join(storeDir, filepath.FromSlash(rel))

		// Content-first: write bytes (idempotently), commit state after.
		if existing, err := os.ReadFile(full); err == nil {
			if index.HashBytes(existing) != sha {
				return clock.New(clock.KindHashConflict, "write",
					"on-disk content at %s does not match computed hash", rel)
			}
			// Identical file already in store; the journal call below stays
			// idempotent and never republishes.
		} else if os.IsNotExist(err) {
			if err := writeAtomic(full, data); err != nil {
				return err
			}
		} else {
			return clock.New(clock.KindIO, "write", "stat %s: %v", rel, err)
		}

		art := index.Artifact{
			StorePath:  rel,
			SHA256:     sha,
			Size:       int64(len(data)),
			RepID:      item.repID,
			SegmentIdx: item.seg.Index,
		}
		if err := r.journal.RegisterArtifact(art); err != nil {
			// InjectedCrash propagates; the process exits 75 without commit.
			return err
		}
		processed++
		if processed%r.cfg.Chunk == 0 {
			if err := r.journal.CheckpointChunk(processed/r.cfg.Chunk,
				r.cfg.Chunk); err != nil {
				return err
			}
		}
	}
	// Final checkpoint for a trailing partial chunk.
	if processed%r.cfg.Chunk != 0 || processed > 0 {
		if err := r.journal.CheckpointChunk((processed+r.cfg.Chunk-1)/r.cfg.Chunk,
			processed%r.cfg.Chunk); err != nil {
			return err
		}
	}
	return r.journal.AdvanceStage(index.StageWrite)
}

// readBounded enforces the 64 MiB per-segment ceiling at read time.
func (r *Runner) readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.New(clock.KindIO, "write", "open %s: %v", path, err)
	}
	defer f.Close()
	data := make([]byte, 0, 4096)
	buf := make([]byte, 64*1024)
	var total int64
	for {
		n, readErr := f.Read(buf)
		total += int64(n)
		if total > manifest.MaxSegmentBytes {
			return nil, clock.New(clock.KindInvalidInput, "write",
				"segment %s exceeds 64 MiB", path)
		}
		data = append(data, buf[:n]...)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, clock.New(clock.KindIO, "write",
				"read %s: %v", path, readErr)
		}
	}
	return data, nil
}

// writeAtomic writes a store object via temp file + rename within storeDir.
func writeAtomic(full string, data []byte) error {
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return clock.New(clock.KindIO, "write", "mkdir: %v", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return clock.New(clock.KindIO, "write", "temp: %v", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return clock.New(clock.KindIO, "write", "write: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return clock.New(clock.KindIO, "write", "fsync: %v", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return clock.New(clock.KindIO, "write", "close: %v", err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return clock.New(clock.KindIO, "write", "rename: %v", err)
	}
	return nil
}
