package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/index"
)

// LoadIndex reads the atomically committed random-access index from outDir.
func LoadIndex(outDir string) (*index.Index, error) {
	path := filepath.Join(outDir, indexFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.New(fault.StageIndex,
			"no committed index; run 'process' first").At(path, -1).Wrap(err)
	}
	ix := &index.Index{}
	if err := json.Unmarshal(data, ix); err != nil {
		return nil, fault.New(fault.StageIndex, "corrupt index").At(path, -1).Wrap(err)
	}
	return ix, nil
}

// QueryIndex returns the latest decodable sync sample not later than t.
// Before-start/after-end conditions are returned as index boundary errors.
func QueryIndex(outDir string, t clock.Ticks) (index.Entry, error) {
	ix, err := LoadIndex(outDir)
	if err != nil {
		return index.Entry{}, err
	}
	e, err := ix.Query(t)
	if err != nil {
		return index.Entry{}, fault.New(fault.StageIndex, "random-access query failed").Wrap(err)
	}
	return e, nil
}
