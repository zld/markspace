package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediapack/internal/clock"
)

// Chunk is a fixed-size block of repaired fragment records persisted as
// one file.
type Chunk struct {
	Index   uint32  `json:"index"`
	Entries []Entry `json:"entries"`
}

// Checksum returns the canonical SHA-256 of the chunk records.
func (c Chunk) Checksum() string {
	data, _ := json.Marshal(c.Entries)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ChunkPath returns the file path of chunk i under dir.
func ChunkPath(dir string, i uint32) string {
	return filepath.Join(dir, fmt.Sprintf("chunk-%06d.json", i))
}

func donePath(dir string, i uint32) string {
	return filepath.Join(dir, fmt.Sprintf("chunk-%06d.done", i))
}

// WriteChunk atomically persists a chunk: data goes to a temporary file
// first, is fsynced and renamed into place, then the done marker carrying
// the checksum is written the same way.
func WriteChunk(dir string, c Chunk) error {
	payload, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := atomicWrite(ChunkPath(dir, c.Index), payload); err != nil {
		return &clock.Error{Cat: clock.CatIO, Msg: "write index chunk", Err: err}
	}
	if err := atomicWrite(donePath(dir, c.Index), []byte(c.Checksum()+"\n")); err != nil {
		return &clock.Error{Cat: clock.CatIO, Msg: "write chunk marker", Err: err}
	}
	return nil
}

// LoadChunk reads and fully validates a chunk: the data file, the done
// marker and the checksum must all agree, otherwise ok is false. Only
// complete chunks are ever reused.
func LoadChunk(dir string, i uint32) (c Chunk, ok bool, err error) {
	data, err := os.ReadFile(ChunkPath(dir, i))
	if err != nil {
		if os.IsNotExist(err) {
			return c, false, nil
		}
		return c, false, &clock.Error{Cat: clock.CatIO, Msg: "read index chunk", Err: err}
	}
	marker, err := os.ReadFile(donePath(dir, i))
	if err != nil {
		return c, false, nil // missing or unreadable marker: incomplete chunk
	}
	if json.Unmarshal(data, &c) != nil || c.Index != i {
		return c, false, nil
	}
	if string(bytes.TrimSpace(marker)) != c.Checksum() {
		return c, false, nil
	}
	return c, true, nil
}

// atomicWrite writes data to a temporary file, fsyncs and renames it over
// path.
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
