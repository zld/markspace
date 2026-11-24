package checkpoint

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LoadOrRecover reads and verifies the index, rebuilding a corrupt index from
// hash-verified chunk sidecars.
func (s *Store) LoadOrRecover(inputSHA, paramsSHA string, chunkCount, chunkSize int) (*Index, Recovery, error) {
	rec := Recovery{ResumedFromChunk: 0}
	fresh := func() *Index {
		return &Index{Version: indexVersion, InputSHA256: inputSHA, ParamsSHA256: paramsSHA,
			ChunkCount: chunkCount, ChunkSize: chunkSize}
	}
	idx := fresh()
	data, err := os.ReadFile(s.IndexPath)
	if errors.Is(err, os.ErrNotExist) {
		r, rerr := s.recoverFromChunks(idx)
		if rerr != nil {
			return nil, rec, rerr
		}
		r.Reason = "no index"
		return idx, r, nil
	}
	if err != nil {
		return nil, rec, err
	}
	rec.IndexExisted = true
	var loaded Index
	if err := json.Unmarshal(data, &loaded); err != nil {
		idx = fresh()
		r, rerr := s.recoverFromChunks(idx)
		if rerr != nil {
			return nil, rec, rerr
		}
		r.IndexExisted = true
		r.IndexCorrupt = true
		r.Reason = "index json corrupt"
		return idx, r, s.Save(idx)
	}
	if inputSHA != "" && loaded.InputSHA256 != "" && inputSHA != loaded.InputSHA256 {
		rec.InputMismatch = true
		rec.Reason = "input digest mismatch"
		rec.RolledBackFrom = loaded.NextChunk
		return fresh(), rec, nil
	}
	if paramsSHA != "" && loaded.ParamsSHA256 != "" && paramsSHA != loaded.ParamsSHA256 {
		rec.InputMismatch = true
		rec.Reason = "parameter digest mismatch"
		rec.RolledBackFrom = loaded.NextChunk
		return fresh(), rec, nil
	}
	idx = &loaded
	if chunkCount > 0 {
		idx.ChunkCount = chunkCount
	}
	if chunkSize > 0 {
		idx.ChunkSize = chunkSize
	}
	_, valid, err := s.verifyPrefix(idx.Chunks)
	if err != nil {
		return nil, rec, err
	}
	rec.RolledBackFrom = idx.NextChunk
	idx.Chunks = valid
	idx.NextChunk = len(valid)
	rec.ResumedFromChunk = idx.NextChunk
	if idx.NextChunk != rec.RolledBackFrom {
		rec.Reason = "chunk hash mismatch; rolled back to nearest valid prefix"
		_ = s.Save(idx)
	}
	return idx, rec, nil
}

func (s *Store) verifyPrefix(chunks []Chunk) (int, []Chunk, error) {
	byIndex := make(map[int]Chunk, len(chunks))
	maxSeen := -1
	for _, c := range chunks {
		byIndex[c.Index] = c
		if c.Index > maxSeen {
			maxSeen = c.Index
		}
	}
	var valid []Chunk
	rolledFrom := len(chunks)
	for i := 0; i <= maxSeen; i++ {
		c, ok := byIndex[i]
		if !ok {
			break
		}
		got, err := HashFile(s.ChunkPath(i))
		if err != nil || got != c.SHA256 {
			rolledFrom = c.Index
			break
		}
		valid = append(valid, c)
	}
	return rolledFrom, valid, nil
}

func (s *Store) recoverFromChunks(idx *Index) (Recovery, error) {
	rec := Recovery{}
	entries, err := os.ReadDir(s.ChunkDir)
	if errors.Is(err, os.ErrNotExist) {
		rec.Reason = "no chunk directory"
		return rec, nil
	}
	if err != nil {
		return rec, err
	}
	byIndex := map[int]Chunk{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "chunk-") || !strings.HasSuffix(name, ".mp4") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "chunk-"), ".mp4"))
		if err != nil {
			continue
		}
		path := filepath.Join(s.ChunkDir, name)
		got, err := HashFile(path)
		if err != nil {
			continue
		}
		expected := got
		if side, err := os.ReadFile(path + ".sha256"); err == nil {
			fields := strings.Fields(string(side))
			if len(fields) > 0 {
				expected = fields[0]
			}
		}
		if got != expected {
			continue
		}
		st, _ := os.Stat(path)
		var size int64
		if st != nil {
			size = st.Size()
		}
		byIndex[n] = Chunk{Index: n, Bytes: size, SHA256: got}
	}
	for i := 0; ; i++ {
		c, ok := byIndex[i]
		if !ok {
			break
		}
		idx.Chunks = append(idx.Chunks, c)
	}
	idx.NextChunk = len(idx.Chunks)
	rec.ResumedFromChunk = idx.NextChunk
	rec.RolledBackFrom = idx.NextChunk
	return rec, nil
}
