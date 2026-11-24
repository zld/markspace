// Package checkpoint persists pipeline commit boundaries atomically and
// validates resumed jobs against the original configuration and input prefix.
// A mismatch is reported with the exact offending input byte offset.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Mismatch reasons surfaced to the CLI exit-code mapping.
var (
	ErrConfigMismatch = errors.New("checkpoint configuration summary mismatch")
	ErrPrefixMismatch = errors.New("input prefix checksum mismatch")
	ErrVersion        = errors.New("unsupported checkpoint version")
)

// Version is bumped whenever the on-disk layout changes incompatibly.
const Version = 1

// State is the full recoverable boundary of a processing job.
type State struct {
	Version        int    `json:"version"`
	JobID          string `json:"job_id"`
	InputPath      string `json:"input_path"`
	ConfigSummary  string `json:"config_summary"`  // sha256 of normalized job parameters
	PrefixHash     string `json:"prefix_hash"`     // sha256 of input bytes [0,ConsumedOffset)
	ConsumedOffset int64  `json:"consumed_offset"` // raw stream bytes already committed
	NextSeq        uint64 `json:"next_seq"`        // reorder frontier at the boundary
	BlocksDone     int    `json:"blocks_done"`     // validated audio blocks committed
	OutputSamples  int64  `json:"output_samples"`  // PCM samples already written
	OutputHash     string `json:"output_hash"`     // running sha256 of the output prefix
}

// MismatchError carries the reason and the byte offset that proved the
// resumed input differs from the checkpointed one.
type MismatchError struct {
	Reason error
	Offset int64
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("%v at offset %d", e.Reason, e.Offset)
}

func (e *MismatchError) Unwrap() error { return e.Reason }

// SummaryHash returns the sha256 hex digest of an opaque configuration blob.
func SummaryHash(configJSON []byte) string {
	sum := sha256.Sum256(configJSON)
	return hex.EncodeToString(sum[:])
}

// PrefixHashOf hashes the given input prefix bytes.
func PrefixHashOf(prefix []byte) string {
	sum := sha256.Sum256(prefix)
	return hex.EncodeToString(sum[:])
}

// Save writes state atomically: temp file in the same directory, fsync, rename.
func Save(path string, st *State) error {
	st.Version = Version
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, ".ckpt-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // harmless once rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Load reads a checkpoint file. A missing file returns (nil, os.IsNotExist).
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	st := &State{}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, err
	}
	if st.Version != Version {
		return nil, fmt.Errorf("%w: got %d want %d", ErrVersion, st.Version, Version)
	}
	return st, nil
}

// Verify checks a candidate resume against the stored boundary. configJSON is
// the same normalized parameter blob used at first run; committedPrefix is the
// input bytes [0, ConsumedOffset) retained by the pipeline and livePrefix is
// the same-length prefix of the input being resumed. A mismatch reports the
// exact byte offset where the two inputs first diverge.
func (s *State) Verify(configJSON, committedPrefix, livePrefix []byte) error {
	if got := SummaryHash(configJSON); got != s.ConfigSummary {
		return &MismatchError{Reason: ErrConfigMismatch, Offset: s.ConsumedOffset}
	}
	if got := PrefixHashOf(livePrefix); got != s.PrefixHash {
		return &MismatchError{Reason: ErrPrefixMismatch, Offset: FirstDiff(committedPrefix, livePrefix)}
	}
	return nil
}

// FirstDiff returns the earliest byte offset where a and b differ, or the
// shorter length when one is a prefix of the other.
func FirstDiff(a, b []byte) int64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return int64(i)
		}
	}
	return int64(n)
}
