// Package checkpoint persists batch progress as fixed-size chunks. Every
// chunk file is written to a temporary path and atomically renamed, and the
// manifest that indexes them is written the same way, so a crash can only
// ever leave uncommitted work behind.
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"mediaaudit/internal/model"
	"mediaaudit/internal/render"
	"mediaaudit/internal/report"
	"mediaaudit/internal/timeline"
)

// Version is the on-disk format version. A different version is refused
// rather than guessed at.
const Version = 1

// Meta is the local index entry for one committed chunk.
type Meta struct {
	Index        int    `json:"index"`
	FirstSeq     int    `json:"first_seq"`
	LastSeq      int    `json:"last_seq"`
	InputDigest  string `json:"input_digest"`
	Digest       string `json:"digest"`
	ItemsDigest  string `json:"items_digest"`
	FramesDigest string `json:"frames_digest"`
}

// Manifest indexes every committed chunk. It contains no timestamps, so two
// runs over the same inputs produce byte-identical manifests.
type Manifest struct {
	Version      int    `json:"version"`
	ChunkSize    int    `json:"chunk_size"`
	TotalChunks  int    `json:"total_chunks"`
	ConfigDigest string `json:"config_digest"`
	Chunks       []Meta `json:"chunks"`
}

// Payload is the persisted body of a chunk; the manifest digest is recomputed
// from exactly these fields.
type Payload struct {
	Index  int                  `json:"index"`
	Items  []timeline.Item      `json:"items"`
	Frames []render.FrameRecord `json:"frames"`
	Errors []*model.Error       `json:"errors,omitempty"`
}

// Store is a checkpoint directory.
type Store struct {
	dir      string
	manifest Manifest
	loaded   bool
}

// ManifestName and ChunkDir are the fixed layout names.
const (
	ManifestName = "checkpoint.json"
	ChunkDir     = "chunks"
)

// Open reads an existing checkpoint directory. A missing manifest is a fresh
// store; a malformed one is a fatal checkpoint error.
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir}
	path := filepath.Join(dir, ManifestName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, model.NewError(model.CodeIO, "checkpoint", "", model.InputPos{File: path}, "%v", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, model.NewError(model.CodeCheckpointCorrupt, "checkpoint", "",
			model.InputPos{File: path}, "manifest is not valid JSON: %v", err)
	}
	if m.Version != Version {
		return nil, model.NewError(model.CodeCheckpointCorrupt, "checkpoint", "",
			model.InputPos{File: path}, "manifest version %d, want %d", m.Version, Version)
	}
	if err := m.validate(); err != nil {
		return nil, model.NewError(model.CodeCheckpointCorrupt, "checkpoint", "",
			model.InputPos{File: path}, "%v", err)
	}
	s.manifest = m
	s.loaded = true
	return s, nil
}

func (m *Manifest) validate() error {
	if m.ChunkSize <= 0 {
		return fmt.Errorf("chunk size %d is invalid", m.ChunkSize)
	}
	if m.TotalChunks < 0 || len(m.Chunks) > m.TotalChunks {
		return fmt.Errorf("chunk count %d exceeds total %d", len(m.Chunks), m.TotalChunks)
	}
	for i, c := range m.Chunks {
		if c.Index != i {
			return fmt.Errorf("chunk %d is out of order at position %d", c.Index, i)
		}
		if c.InputDigest == "" || c.Digest == "" {
			return fmt.Errorf("chunk %d has an empty digest", i)
		}
	}
	return nil
}

// Loaded reports whether an on-disk manifest was read.
func (s *Store) Loaded() bool { return s.loaded }

// Manifest returns the current in-memory index.
func (s *Store) Manifest() Manifest { return s.manifest }

// ConfigDigest returns the digest recorded by the existing checkpoint.
func (s *Store) ConfigDigest() string { return s.manifest.ConfigDigest }

// Committed counts chunks recorded in the manifest.
func (s *Store) Committed() int { return len(s.manifest.Chunks) }

// Reset removes the manifest and every chunk file, starting a clean run.
func (s *Store) Reset() error {
	if err := os.Remove(filepath.Join(s.dir, ManifestName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return model.NewError(model.CodeIO, "checkpoint", "", model.InputPos{File: s.dir}, "%v", err)
	}
	if err := os.RemoveAll(filepath.Join(s.dir, ChunkDir)); err != nil {
		return model.NewError(model.CodeIO, "checkpoint", "", model.InputPos{File: s.dir}, "%v", err)
	}
	s.manifest = Manifest{Version: Version}
	s.loaded = false
	return nil
}

// Begin validates the checkpoint against this run's configuration and
// prepares the manifest. When resume is false any previous progress is
// discarded. When resume is true a different configuration digest is fatal.
func (s *Store) Begin(chunkSize, totalChunks int, configDigest string, resume bool) error {
	if resume && s.loaded {
		if s.manifest.ConfigDigest != configDigest {
			return model.NewError(model.CodeCheckpointMismatch, "checkpoint", "",
				model.InputPos{File: filepath.Join(s.dir, ManifestName)},
				"checkpoint was built with config %s but this run uses %s; refusing to resume",
				s.manifest.ConfigDigest, configDigest)
		}
		if s.manifest.ChunkSize != chunkSize {
			return model.NewError(model.CodeCheckpointMismatch, "checkpoint", "",
				model.InputPos{File: filepath.Join(s.dir, ManifestName)},
				"checkpoint chunk size %d, run uses %d", s.manifest.ChunkSize, chunkSize)
		}
		if s.manifest.TotalChunks != totalChunks {
			return model.NewError(model.CodeCheckpointMismatch, "checkpoint", "",
				model.InputPos{File: filepath.Join(s.dir, ManifestName)},
				"checkpoint covers %d chunks, run needs %d", s.manifest.TotalChunks, totalChunks)
		}
		return nil
	}
	if err := s.Reset(); err != nil {
		return err
	}
	s.manifest = Manifest{Version: Version, ChunkSize: chunkSize, TotalChunks: totalChunks, ConfigDigest: configDigest}
	// The manifest now exists on disk, so a later Begin(resume=true) on this
	// same store must validate against it.
	s.loaded = true
	if err := os.MkdirAll(filepath.Join(s.dir, ChunkDir), 0o755); err != nil {
		return model.NewError(model.CodeIO, "checkpoint", "", model.InputPos{File: s.dir}, "%v", err)
	}
	return s.saveManifest()
}

// Meta returns the committed entry for a chunk.
func (s *Store) Meta(index int) (Meta, bool) {
	if index < 0 || index >= len(s.manifest.Chunks) {
		return Meta{}, false
	}
	return s.manifest.Chunks[index], true
}

// ChunkPath returns the chunk file path.
func (s *Store) ChunkPath(index int) string {
	return filepath.Join(s.dir, ChunkDir, "chunk-"+pad5(index)+".json")
}

func pad5(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 5 {
		s = "0" + s
	}
	return s
}

// Commit writes one chunk's payload atomically and then updates the manifest.
// Payload fields are re-derived digests; the caller supplies the digest of
// the chunk's inputs so resume can prove nothing upstream changed.
func (s *Store) Commit(chunk report.Chunk, inputDigest string) (Meta, error) {
	if chunk.Index < 0 || chunk.Index >= s.manifest.TotalChunks {
		return Meta{}, model.NewError(model.CodeInvalidArgument, "checkpoint", "",
			model.InputPos{}, "chunk index %d outside 0..%d", chunk.Index, s.manifest.TotalChunks-1)
	}
	p := Payload{Index: chunk.Index, Items: chunk.Items, Frames: chunk.Frames, Errors: chunk.Errors}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return Meta{}, model.NewError(model.CodeInternal, "checkpoint", "", model.InputPos{}, "%v", err)
	}
	// Digests are derived here rather than trusted from the caller, so the
	// values on disk are exactly what Verify will recompute.
	itemsDigest := report.ItemsDigest(p.Items)
	framesDigest := report.FramesDigest(p.Frames)
	first, last := 0, 0
	if len(p.Items) > 0 {
		first = p.Items[0].Seq
		last = p.Items[len(p.Items)-1].Seq
	}
	meta := Meta{
		Index: chunk.Index, FirstSeq: first, LastSeq: last,
		InputDigest: inputDigest,
		Digest:      report.InputDigest(itemsDigest, framesDigest, inputDigest),
		ItemsDigest: itemsDigest, FramesDigest: framesDigest,
	}
	if err := os.MkdirAll(filepath.Join(s.dir, ChunkDir), 0o755); err != nil {
		return Meta{}, model.NewError(model.CodeIO, "checkpoint", "", model.InputPos{File: s.dir}, "%v", err)
	}
	if err := writeAtomic(s.ChunkPath(chunk.Index), data); err != nil {
		return Meta{}, err
	}
	// Extend or replace the manifest entry, then persist it atomically.
	if chunk.Index < len(s.manifest.Chunks) {
		s.manifest.Chunks[chunk.Index] = meta
	} else {
		for len(s.manifest.Chunks) < chunk.Index {
			return Meta{}, model.NewError(model.CodeCheckpointCorrupt, "checkpoint", "",
				model.InputPos{}, "cannot commit chunk %d: chunk %d is missing", chunk.Index, len(s.manifest.Chunks))
		}
		s.manifest.Chunks = append(s.manifest.Chunks, meta)
	}
	if err := s.saveManifest(); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// Verify re-reads a committed chunk, recomputes its digests from the stored
// payload and compares them with the manifest. It also checks that the input
// digest witnessed on disk still matches the inputs of this run.
func (s *Store) Verify(index int, wantInputDigest string) (bool, error) {
	meta, ok := s.Meta(index)
	if !ok {
		return false, model.NewError(model.CodeCheckpointCorrupt, "checkpoint", "",
			model.InputPos{}, "chunk %d is not committed", index)
	}
	if meta.InputDigest != wantInputDigest {
		return false, model.NewError(model.CodeCheckpointMismatch, "checkpoint", "",
			model.InputPos{File: s.ChunkPath(index)},
			"chunk %d input digest %s does not match this run's %s; refusing to resume",
			index, meta.InputDigest, wantInputDigest)
	}
	p, err := s.ReadPayload(index)
	if err != nil {
		return false, err
	}
	if p.Index != index {
		return false, model.NewError(model.CodeCheckpointCorrupt, "checkpoint", "",
			model.InputPos{File: s.ChunkPath(index)}, "payload index %d, want %d", p.Index, index)
	}
	items := report.ItemsDigest(p.Items)
	frames := report.FramesDigest(p.Frames)
	combined := report.InputDigest(items, frames, wantInputDigest)
	if items != meta.ItemsDigest || frames != meta.FramesDigest || combined != meta.Digest {
		return false, model.NewError(model.CodeCheckpointCorrupt, "checkpoint", "",
			model.InputPos{File: s.ChunkPath(index)}, "chunk %d content does not match its manifest digests", index)
	}
	return true, nil
}

// ReadPayload decodes a committed chunk file.
func (s *Store) ReadPayload(index int) (Payload, error) {
	var p Payload
	data, err := os.ReadFile(s.ChunkPath(index))
	if err != nil {
		return p, model.NewError(model.CodeCheckpointCorrupt, "checkpoint", "",
			model.InputPos{File: s.ChunkPath(index)}, "cannot read committed chunk: %v", err)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, model.NewError(model.CodeCheckpointCorrupt, "checkpoint", "",
			model.InputPos{File: s.ChunkPath(index)}, "chunk is not valid JSON: %v", err)
	}
	return p, nil
}

// saveManifest writes the manifest atomically.
func (s *Store) saveManifest() error {
	data, err := json.MarshalIndent(s.manifest, "", "  ")
	if err != nil {
		return model.NewError(model.CodeInternal, "checkpoint", "", model.InputPos{}, "%v", err)
	}
	return writeAtomic(filepath.Join(s.dir, ManifestName), data)
}

// writeAtomic writes data to path via a same-directory temporary file so the
// final rename cannot cross filesystems, fsyncs it, renames it into place and
// fsyncs the directory entry.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return model.NewError(model.CodeIO, "checkpoint", "", model.InputPos{File: path}, "%v", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return model.NewError(model.CodeIO, "checkpoint", "", model.InputPos{File: tmpName}, "%v", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return model.NewError(model.CodeIO, "checkpoint", "", model.InputPos{File: tmpName}, "%v", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return model.NewError(model.CodeIO, "checkpoint", "", model.InputPos{File: tmpName}, "%v", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return model.NewError(model.CodeIO, "checkpoint", "", model.InputPos{File: tmpName}, "%v", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return model.NewError(model.CodeIO, "checkpoint", "", model.InputPos{File: path}, "%v", err)
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
