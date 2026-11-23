// Package pipeline orchestrates the staged QC batch: parse -> buffer ->
// recovery -> sync -> normalize -> package. It maintains per-material
// checkpoints for deterministic reruns, isolates failures between materials
// and validates cross-stage invariants.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"mediaqc/internal/model"
)

// StageName enumerates pipeline stages in execution order.
var StageName = []model.Stage{
	model.StageParse,
	model.StageBuffer,
	model.StageRecovery,
	model.StageSync,
	model.StageNormalize,
	model.StagePackage,
}

// Checkpoint is the serialized state of one material at one stage boundary.
// It contains only deterministic data, never wall-clock timings.
type Checkpoint struct {
	MaterialID   string          `json:"material_id"`
	Stage        model.Stage     `json:"stage"`
	EventCount   int             `json:"event_count"`
	Sequences    []uint64        `json:"sequences"`
	Gaps         []model.Gap     `json:"gaps"`
	RecoveredSeq []uint64        `json:"recovered_sequences"`
	Status       string          `json:"status"`
	PayloadHash  string          `json:"payload_hash"`
}

// Hash gives a stable content hash so two identical runs compare equal.
func (c Checkpoint) Hash() string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Checkpointer stores stage checkpoints keyed by material+stage.
type Checkpointer struct {
	points map[string]Checkpoint
}

// NewCheckpointer creates an empty checkpointer.
func NewCheckpointer() *Checkpointer {
	return &Checkpointer{points: map[string]Checkpoint{}}
}

func key(materialID string, stage model.Stage) string { return materialID + "|" + string(stage) }

// Save records a checkpoint, rejecting an inconsistent overwrite.
func (c *Checkpointer) Save(cp Checkpoint) error {
	k := key(cp.MaterialID, cp.Stage)
	if existing, ok := c.points[k]; ok && existing.Hash() != cp.Hash() {
		return model.NewError(model.CodeInvariant, model.StageCheckpoint, cp.MaterialID, "",
			"non-deterministic checkpoint overwrite at stage "+string(cp.Stage))
	}
	c.points[k] = cp
	return nil
}

// Load retrieves a checkpoint.
func (c *Checkpointer) Load(materialID string, stage model.Stage) (Checkpoint, bool) {
	cp, ok := c.points[key(materialID, stage)]
	return cp, ok
}

// All returns checkpoints sorted by material then stage order for hashing.
func (c *Checkpointer) All() []Checkpoint {
	out := make([]Checkpoint, 0, len(c.points))
	for _, cp := range c.points {
		out = append(out, cp)
	}
	stageRank := map[model.Stage]int{}
	for i, s := range StageName {
		stageRank[s] = i
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MaterialID != out[j].MaterialID {
			return out[i].MaterialID < out[j].MaterialID
		}
		return stageRank[out[i].Stage] < stageRank[out[j].Stage]
	})
	return out
}

// Digest hashes every checkpoint; identical inputs must yield identical
// digests across separate runs.
func (c *Checkpointer) Digest() string {
	b, _ := json.Marshal(c.All())
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
