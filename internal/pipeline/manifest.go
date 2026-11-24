package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"media-archiver/internal/media"
	"media-archiver/internal/repair"
	"media-archiver/internal/verify"
)

const ManifestName = "manifest.json"

// ProcessingConfig is the deterministic configuration captured in manifest.
type ProcessingConfig struct {
	Seed            int64         `json:"seed"`
	Injection       string        `json:"injection"`
	Policy          repair.Policy `json:"policy"`
	MaxBlockBytes   int64         `json:"max_block_bytes"`
	MaxIndexEntries int           `json:"max_index_entries"`
}

// StageState persists one phase's checkpoint.
type StageState struct {
	Name         media.Stage `json:"name"`
	Status       string      `json:"status"`
	Reused       bool        `json:"reused,omitempty"`
	OutputSHA256 string      `json:"output_sha256,omitempty"`
}

// Failure is the deterministic failure record in a non-success manifest.
type Failure struct {
	ErrorCode string `json:"error_code"`
	Stage     string `json:"stage"`
	Offset    int64  `json:"corrupt_offset"`
	Reason    string `json:"reason"`
}

// Manifest is the local checkpoint and source-binding document.
type Manifest struct {
	SchemaVersion       int              `json:"schema_version"`
	Status              string           `json:"status"`
	InputRelativePath   string           `json:"input_relative_path"`
	InputSHA256         string           `json:"input_sha256"`
	InputSize           int64            `json:"input_size"`
	ConfigSHA256        string           `json:"config_sha256"`
	Config              ProcessingConfig `json:"config"`
	Format              media.PCMFormat  `json:"format,omitempty"`
	AudioSHA256         string           `json:"audio_sha256,omitempty"`
	Stages              []StageState     `json:"stages"`
	LastCompletedStage  string           `json:"last_completed_stage,omitempty"`
	IndexSHA256         string           `json:"index_sha256,omitempty"`
	IndexEntries        int              `json:"index_entries,omitempty"`
	TotalFrames         int64            `json:"total_frames,omitempty"`
	ManifestAlignmentNS int64            `json:"manifest_alignment_ns"`
	AlignmentBoundNS    int64            `json:"alignment_bound_ns"`
	Stats               *verify.Stats    `json:"stats,omitempty"`
	Failure             *Failure         `json:"failure,omitempty"`
}

func manifestPath(workdir string) string { return filepath.Join(workdir, ManifestName) }

func newManifest(cfg ProcessingConfig, inputSHA string, inputSize int64, configSHA string, alignmentBound int64) *Manifest {
	return &Manifest{
		SchemaVersion:     1,
		Status:            "running",
		InputRelativePath: filepath.Join("input", "sample.wav"),
		InputSHA256:       inputSHA,
		InputSize:         inputSize,
		ConfigSHA256:      configSHA,
		Config:            cfg,
		Stages: []StageState{
			{Name: media.StageParse, Status: "pending"},
			{Name: media.StageTransform, Status: "pending"},
			{Name: media.StageIndex, Status: "pending"},
			{Name: media.StageVerify, Status: "pending"},
		},
		AlignmentBoundNS: alignmentBound,
	}
}

func loadManifest(workdir string) (*Manifest, error) {
	data, err := os.ReadFile(manifestPath(workdir))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, media.NewError(media.CodeInternal, "", -1, "parse manifest", err)
	}
	return &m, nil
}

func saveManifest(workdir string, m *Manifest) error {
	data, err := media.MarshalCanonicalJSON(m)
	if err != nil {
		return err
	}
	return media.WriteFileAtomic(manifestPath(workdir), data, 0o644)
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (m *Manifest) stage(name media.Stage) *StageState {
	for i := range m.Stages {
		if m.Stages[i].Name == name {
			return &m.Stages[i]
		}
	}
	return nil
}

func (m *Manifest) setStage(name media.Stage, status string, reused bool, sha string) {
	s := m.stage(name)
	if s == nil {
		m.Stages = append(m.Stages, StageState{Name: name})
		s = &m.Stages[len(m.Stages)-1]
	}
	s.Status = status
	s.Reused = reused
	s.OutputSHA256 = sha
	if status == "completed" {
		m.LastCompletedStage = string(name)
	}
}
