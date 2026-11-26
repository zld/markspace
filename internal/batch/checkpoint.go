package batch

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediarepair/internal/fsx"
	"mediarepair/internal/media"
)

const CheckpointFormatVersion = 1

// Checkpoint is the atomic progress marker. The index is written before this
// file; therefore this file records the durable commit point.
type Checkpoint struct {
	Version          int    `json:"version"`
	InputFingerprint string `json:"input_fingerprint"`
	BlockSize        int    `json:"block_size"`
	CommittedBlocks  int    `json:"committed_blocks"`
	ProcessedFrames  int    `json:"processed_frames"`
	IndexSHA256      string `json:"index_sha256"`
	Complete         bool   `json:"complete"`
}

func checkpointPath(root string) string {
	return filepath.Join(root, "work", "checkpoint.json")
}

func indexPath(root string) string {
	return filepath.Join(root, "work", "index.json")
}

func segmentPath(root string, block int) string {
	return filepath.Join(root, "segments", "segment-"+threeDigits(block)+".bin")
}

func stagePath(root string, block int) string {
	return filepath.Join(root, "work", "stage", "segment-"+threeDigits(block)+".tmp")
}

func LoadCheckpoint(root string) (*Checkpoint, error) {
	b, err := os.ReadFile(checkpointPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, media.E(media.CodeWrite, "checkpoint.read", err).WithFile(checkpointPath(root))
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, media.E(media.CodeCheckpoint, "checkpoint.parse", err).WithFile(checkpointPath(root))
	}
	if cp.Version != CheckpointFormatVersion || cp.BlockSize <= 0 {
		return nil, media.E(media.CodeCheckpoint, "checkpoint.version", nil).WithFile(checkpointPath(root))
	}
	return &cp, nil
}

func SaveCheckpoint(root string, cp *Checkpoint) error {
	cp.Version = CheckpointFormatVersion
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return media.E(media.CodeWrite, "checkpoint.encode", err)
	}
	b = append(b, '\n')
	return fsx.AtomicWriteFile(checkpointPath(root), bytes.NewReader(b), 0o644)
}

func DiscardUncommittedTails(root string, committedBlocks int) error {
	stageDir := filepath.Join(root, "work", "stage")
	if err := os.RemoveAll(stageDir); err != nil {
		return media.E(media.CodeWrite, "checkpoint.clean_stage", err).WithFile(stageDir)
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return media.E(media.CodeWrite, "checkpoint.mkdir_stage", err).WithFile(stageDir)
	}
	segDir := filepath.Join(root, "segments")
	entries, err := os.ReadDir(segDir)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(segDir, 0o755)
	}
	if err != nil {
		return media.E(media.CodeWrite, "checkpoint.read_segments", err).WithFile(segDir)
	}
	for _, ent := range entries {
		var block int
		if _, scanErr := parseIntSegmentName(ent.Name(), &block); scanErr != nil {
			continue
		}
		if block >= committedBlocks {
			if err := os.Remove(filepath.Join(segDir, ent.Name())); err != nil {
				return media.E(media.CodeWrite, "checkpoint.remove_tail", err).WithFile(ent.Name())
			}
		}
	}
	return nil
}

func threeDigits(v int) string {
	switch {
	case v < 10:
		return "00" + itoa(v)
	case v < 100:
		return "0" + itoa(v)
	default:
		return itoa(v)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func parseIntSegmentName(name string, block *int) (bool, error) {
	const prefix = "segment-"
	const suffix = ".bin"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false, nil
	}
	n, err := strconv.Atoi(name[len(prefix) : len(name)-len(suffix)])
	if err != nil {
		return false, err
	}
	*block = n
	return true, nil
}
