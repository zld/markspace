// Package pipeline orchestrates scanning, repair, index rebuild, verification
// and provenance for many inputs. It bounds in-flight work with a fixed queue,
// propagates downstream failures upstream, and persists checkpoints so an
// interrupted run resumes from its last complete stage.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"archivist/internal/fault"
)

// Stage names the durable steps of one input's processing. The order is the
// contract for resume: a stage may only be reused when every earlier stage
// is also complete and its artifact still matches its recorded digest.
type Stage string

const (
	StageScan       Stage = "scan"
	StageRepair     Stage = "repair"
	StageWrite      Stage = "write"
	StageIndex      Stage = "index"
	StageVerify     Stage = "verify"
	StageProvenance Stage = "provenance"
)

// stageOrder is the canonical pipeline sequence.
var stageOrder = []Stage{
	StageScan, StageRepair, StageWrite, StageIndex, StageVerify, StageProvenance,
}

// stageVersion pins the implementation of each stage. Changing a stage's
// version invalidates checkpoints that completed it.
var stageVersion = map[Stage]string{
	StageScan:       "scan/v1",
	StageRepair:     "repair/v1",
	StageWrite:      "write/v1",
	StageIndex:      "index/v1",
	StageVerify:     "verify/v1",
	StageProvenance: "provenance/v1",
}

// CheckpointVersion guards the on-disk checkpoint schema.
const CheckpointVersion = "checkpoint/v1"

// Params are the run parameters that affect output bytes. They are hashed
// into the checkpoint identity, so a parameter change starts a fresh run.
type Params struct {
	QueueCapacity  int    `json:"queue_capacity"`
	ChunkSamples   int    `json:"chunk_samples"`
	MovieTimescale uint32 `json:"movie_timescale"`
	RepairLimit    int64  `json:"repair_limit"`
	RulesVersion   string `json:"rules_version"`
}

// Hash returns a stable digest of the parameters plus every stage version.
func (p Params) Hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "queue=%d\nchunk=%d\nmovie_ts=%d\nrepair_limit=%d\nrules=%s\n",
		p.QueueCapacity, p.ChunkSamples, p.MovieTimescale, p.RepairLimit, p.RulesVersion)
	stages := make([]string, 0, len(stageVersion))
	for s := range stageVersion {
		stages = append(stages, string(s))
	}
	sort.Strings(stages)
	for _, s := range stages {
		fmt.Fprintf(h, "%s=%s\n", s, stageVersion[Stage(s)])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Artifact records a produced file and its digest.
type Artifact struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Checkpoint is the durable progress record for one input and parameter set.
type Checkpoint struct {
	Version     string           `json:"version"`
	InputName   string           `json:"input_name"`
	InputPath   string           `json:"input_path"`
	InputSize   int64            `json:"input_size"`
	InputSHA256 string           `json:"input_sha256"`
	Params      Params           `json:"params"`
	ParamsHash  string           `json:"params_hash"`
	Completed   []Stage          `json:"completed"`
	StageVers   map[Stage]string `json:"stage_versions"`

	Scan             Artifact `json:"scan"`
	Repair           Artifact `json:"repair"`
	Output           Artifact `json:"output"`
	Index            Artifact `json:"index"`
	Provenance       Artifact `json:"provenance"`
	ProvenanceDigest string   `json:"provenance_digest"`
	RepairedEntries  int      `json:"repaired_entries"`
	IndexEntries     int      `json:"index_entries"`
	KeyframeEntries  int      `json:"keyframe_entries"`
	Status           string   `json:"status"`
}

// LastStage returns the last completed stage, or the empty string.
func (c *Checkpoint) LastStage() Stage {
	if len(c.Completed) == 0 {
		return ""
	}
	return c.Completed[len(c.Completed)-1]
}

// Has reports whether s completed.
func (c *Checkpoint) Has(s Stage) bool {
	for _, got := range c.Completed {
		if got == s {
			return true
		}
	}
	return false
}

// MarkComplete appends s once, preserving pipeline order.
func (c *Checkpoint) MarkComplete(s Stage) {
	if c.Has(s) {
		return
	}
	c.Completed = append(c.Completed, s)
	sort.Slice(c.Completed, func(i, j int) bool {
		return stageRank(c.Completed[i]) < stageRank(c.Completed[j])
	})
}

// TruncateAfter drops every stage after s, used when a later artifact fails
// verification and must be regenerated.
func (c *Checkpoint) TruncateAfter(s Stage) {
	limit := stageRank(s)
	var kept []Stage
	for _, got := range c.Completed {
		if stageRank(got) <= limit {
			kept = append(kept, got)
		}
	}
	c.Completed = kept
}

func stageRank(s Stage) int {
	for i, v := range stageOrder {
		if v == s {
			return i
		}
	}
	return len(stageOrder)
}

// checkpointPath derives a stable filename from the input and parameters.
func checkpointPath(dir, inputName, inputSHA, paramsHash string) string {
	short := inputSHA
	if len(short) > 16 {
		short = short[:16]
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%s-%s.json", inputName, short, paramsHash[:16]))
}

// loadCheckpoint reads and validates a checkpoint file.
func loadCheckpoint(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.New(fault.KindIO, fault.StagePipeline, "read checkpoint").In(path).Wrap(err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fault.New(fault.KindInvariant, fault.StagePipeline, "parse checkpoint").In(path).Wrap(err)
	}
	if cp.Version != CheckpointVersion {
		return nil, nil
	}
	return &cp, nil
}

// save writes the checkpoint atomically so a crash mid-write cannot leave a
// half-parsed file that would be mistaken for progress.
func (c *Checkpoint) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fault.New(fault.KindIO, fault.StagePipeline, "create checkpoint dir").In(path).Wrap(err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fault.New(fault.KindInvariant, fault.StagePipeline, "encode checkpoint").Wrap(err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fault.New(fault.KindIO, fault.StagePipeline, "write checkpoint").In(tmp).Wrap(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fault.New(fault.KindIO, fault.StagePipeline, "publish checkpoint").In(path).Wrap(err)
	}
	return nil
}

// hashFile returns the SHA-256 and size of path.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fault.New(fault.KindIO, fault.StageRead, "open for digest").In(path).Wrap(err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(h, f, make([]byte, 64*1024))
	if err != nil {
		return "", 0, fault.New(fault.KindIO, fault.StageRead, "digest file").In(path).Wrap(err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// verifyArtifact confirms a recorded artifact still exists with its digest.
func verifyArtifact(a Artifact) error {
	if a.Path == "" || a.SHA256 == "" {
		return fault.New(fault.KindInvariant, fault.StageVerify, "artifact was never recorded")
	}
	sum, size, err := hashFile(a.Path)
	if err != nil {
		return err
	}
	if sum != a.SHA256 {
		return fault.New(fault.KindInvariant, fault.StageVerify,
			"artifact %s digest changed: recorded %s, found %s", a.Path, a.SHA256, sum).In(a.Path)
	}
	if a.Size != 0 && size != a.Size {
		return fault.New(fault.KindInvariant, fault.StageVerify,
			"artifact %s size changed: recorded %d, found %d", a.Path, a.Size, size).In(a.Path)
	}
	return nil
}

// ValidateAgainst checks that a loaded checkpoint belongs to this input and
// parameter set, and drops stages whose version has changed.
func (c *Checkpoint) ValidateAgainst(inputSHA string, params Params) bool {
	if c == nil || c.InputSHA256 != inputSHA || c.ParamsHash != params.Hash() {
		return false
	}
	for _, s := range c.Completed {
		if c.StageVers[s] != stageVersion[s] {
			return false
		}
	}
	return true
}
