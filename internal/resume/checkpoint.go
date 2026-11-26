package resume

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"continuity/internal/atomicfile"
	"continuity/internal/media"
)

const checkpointName = "checkpoint.json"

// Binding is the triple that must match before committed work is reused.
type Binding struct {
	InputPrefixSHA256 string `json:"input_prefix_sha256"`
	InputPrefixBytes  int64  `json:"input_prefix_bytes"`
	ManifestVersion   string `json:"manifest_version"`
	ConfigSHA256      string `json:"config_sha256"`
}

// Checkpoint records committed clusters and their output checksums.
type Checkpoint struct {
	Binding
	JobID             string         `json:"job_id"`
	CommittedClusters []int          `json:"committed_clusters"`
	ClusterSHA256     map[int]string `json:"cluster_sha256,omitempty"`
	Stage             string         `json:"stage"`
}

// CheckpointPath returns the canonical checkpoint location under dir.
func CheckpointPath(dir string) string { return filepath.Join(dir, checkpointName) }

// LoadCheckpoint reads a checkpoint. A missing checkpoint returns nil,nil.
func LoadCheckpoint(dir string) (*Checkpoint, error) {
	path := CheckpointPath(dir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, media.NewError(media.StageCheckpoint, 0, path, err).WithKind("read")
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, media.NewError(media.StageCheckpoint, 0, path, err).WithKind("json")
	}
	if cp.ClusterSHA256 == nil {
		cp.ClusterSHA256 = map[int]string{}
	}
	return &cp, nil
}

// Save atomically writes the checkpoint and fsyncs the destination directory.
func (c *Checkpoint) Save(dir string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return media.NewError(media.StageCheckpoint, -1, checkpointName, err).WithKind("marshal")
	}
	path := CheckpointPath(dir)
	if err := atomicfile.Write(path, data, 0o644); err != nil {
		return media.NewError(media.StageCheckpoint, 0, path, err).WithKind("atomic_save")
	}
	return nil
}

// VerifyBinding checks the input prefix, manifest version, and config hash.
// It returns a classified conflict before any committed output is replaced.
func (c *Checkpoint) VerifyBinding(expected Binding) error {
	if c.InputPrefixSHA256 != expected.InputPrefixSHA256 || c.InputPrefixBytes != expected.InputPrefixBytes {
		return media.NewError(media.StageCheckpoint, -1, "input_prefix", media.ErrInputChanged).
			WithKind("prefix_checksum")
	}
	if c.ManifestVersion != expected.ManifestVersion {
		return media.NewError(media.StageCheckpoint, -1, "manifest_version", media.ErrManifestChanged).
			WithKind(c.ManifestVersion + "!=" + expected.ManifestVersion)
	}
	if c.ConfigSHA256 != expected.ConfigSHA256 {
		return media.NewError(media.StageCheckpoint, -1, "config_sha256", media.ErrConfigChanged).
			WithKind("policy_or_config_changed")
	}
	return nil
}

// IsClusterCommitted reports whether index is in the committed set.
func (c *Checkpoint) IsClusterCommitted(index int) bool {
	for _, i := range c.CommittedClusters {
		if i == index {
			return true
		}
	}
	return false
}

// MarkCluster adds a committed cluster and its output checksum.
func (c *Checkpoint) MarkCluster(index int, outputSHA string) {
	if !c.IsClusterCommitted(index) {
		c.CommittedClusters = append(c.CommittedClusters, index)
	}
	if c.ClusterSHA256 == nil {
		c.ClusterSHA256 = map[int]string{}
	}
	c.ClusterSHA256[index] = outputSHA
}
