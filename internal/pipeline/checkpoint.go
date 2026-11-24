package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"mediapipe/internal/model"
	"mediapipe/internal/segment"
	"mediapipe/internal/stream"
)

const CheckpointVersion = 1

// Checkpoint is the complete resumable processing state at a full packet block.
type Checkpoint struct {
	Version             int                       `json:"version"`
	InputPath           string                    `json:"input_path"`
	InputSHA256         string                    `json:"input_sha256"`
	ProcessedPackets    int64                     `json:"processed_packets"`
	SegmentMs           int64                     `json:"segment_ms"`
	CheckpointPackets   int                       `json:"checkpoint_packets"`
	ReorderWindow       int                       `json:"reorder_window"`
	SilenceWindowMs     int64                     `json:"silence_window_ms"`
	FingerprintWindowMs int64                     `json:"fingerprint_window_ms"`
	TimeToleranceMs     int64                     `json:"time_tolerance_ms"`
	Segments            []model.Segment           `json:"segments"`
	Windows             []model.SilenceWindow     `json:"windows"`
	Fingerprints        []model.WindowFingerprint `json:"fingerprints"`
	Mappings            []model.DedupeMapping     `json:"mappings"`
	Health              stream.Health             `json:"health"`
	MaxAlignmentErrorMs int64                     `json:"max_alignment_error_ms"`
}

func checkpointPath(dir string) string {
	return filepath.Join(dir, "checkpoint.json")
}

func previousCheckpointPath(dir string) string {
	return filepath.Join(dir, "checkpoint.prev.json")
}

func indexPath(dir string) string {
	return filepath.Join(dir, "index.mp4")
}

// SaveCheckpoint rotates the current checkpoint to .prev and atomically writes
// the new checkpoint and MP4 index.
func SaveCheckpoint(dir string, cp *Checkpoint) error {
	if cp.Version == 0 {
		cp.Version = CheckpointVersion
	}
	current := checkpointPath(dir)
	if data, err := os.ReadFile(current); err == nil && len(data) > 0 {
		if err := segment.AtomicWriteFile(previousCheckpointPath(dir), data); err != nil {
			return fmt.Errorf("rotate checkpoint: %w", err)
		}
	}
	idx := segment.Index{
		Version:         segment.IndexFormatVersion,
		InputSHA256:     cp.InputSHA256,
		ProcessedPacket: cp.ProcessedPackets,
		Complete:        false,
		Segments:        cp.Segments,
		Mappings:        cp.Mappings,
	}
	if err := segment.WriteMP4Index(indexPath(dir), idx); err != nil {
		return fmt.Errorf("write MP4 index: %w", err)
	}
	if err := segment.WriteJSON(current, cp); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	return nil
}

// LoadLatestCheckpoint validates the current file and falls back to the most
// recent previous complete checkpoint if the current one is incomplete/corrupt.
func LoadLatestCheckpoint(dir string) (*Checkpoint, string, error) {
	current := checkpointPath(dir)
	var cp Checkpoint
	if err := segment.ReadJSON(current, &cp); err == nil {
		if validateCheckpoint(&cp) == nil {
			return &cp, "checkpoint", nil
		}
	} else if !os.IsNotExist(err) {
		// A corrupt current checkpoint is handled by trying the backup.
		_ = err
	}

	prev := previousCheckpointPath(dir)
	var old Checkpoint
	if err := segment.ReadJSON(prev, &old); err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("no valid checkpoint: %w", err)
		}
		return nil, "", fmt.Errorf("read checkpoint: %w", err)
	}
	if err := validateCheckpoint(&old); err != nil {
		return nil, "", err
	}
	return &old, "previous_checkpoint", nil
}

func validateCheckpoint(cp *Checkpoint) error {
	if cp.Version != CheckpointVersion {
		return fmt.Errorf("unsupported checkpoint version %d", cp.Version)
	}
	if cp.InputSHA256 == "" {
		return fmt.Errorf("checkpoint missing input digest")
	}
	if cp.ProcessedPackets < 0 {
		return fmt.Errorf("negative processed packet count")
	}
	return nil
}

// RecoverIndexFromCheckpoint rewrites index.mp4 after a missing/incomplete
// index and reports the checkpoint packet used.
func RecoverIndexFromCheckpoint(dir string, cp *Checkpoint) error {
	idx := segment.Index{
		Version:         segment.IndexFormatVersion,
		InputSHA256:     cp.InputSHA256,
		ProcessedPacket: cp.ProcessedPackets,
		Complete:        false,
		Segments:        cp.Segments,
		Mappings:        cp.Mappings,
	}
	return segment.WriteMP4Index(indexPath(dir), idx)
}
