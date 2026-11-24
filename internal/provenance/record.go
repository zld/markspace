// Package provenance writes immutable execution records linking every
// output chunk back to the manifest entry, source segment, parser version
// and processing parameters that produced it.
package provenance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"mediad/internal/model"
)

// ChunkRecord is one traceable output chunk.
type ChunkRecord struct {
	ChunkID        string         `json:"chunk_id"`
	Hash           string         `json:"hash"`
	Representation string         `json:"representation"`
	Start          model.PlayTime `json:"start_ns"`
	End            model.PlayTime `json:"end_ns"`
	KeyFramePTS    model.PlayTime `json:"keyframe_pts_ns"`
	Sources        []SourceRef    `json:"sources"`
	PrimaryOf      []string       `json:"primary_of,omitempty"` // duplicate chunk ids
}

// SourceRef points at the input media that fed a chunk.
type SourceRef struct {
	Manifest       string `json:"manifest"`
	Representation string `json:"representation"`
	Entry          int    `json:"entry"`
	File           string `json:"file"`
}

// RunRecord is the full provenance bundle for one case.
type RunRecord struct {
	CaseID         string                 `json:"case_id"`
	ParserVersions map[string]string      `json:"parser_versions"`
	Parameters     map[string]string      `json:"parameters"`
	Chunks         []ChunkRecord          `json:"chunks"`
	Silence        []SilenceRecord        `json:"silence,omitempty"`
	Errors         []model.SourceError    `json:"errors,omitempty"`
}

// SilenceRecord attaches the threshold parameters to each interval.
type SilenceRecord struct {
	Representation string                 `json:"representation"`
	Intervals      []map[string]any       `json:"intervals"`
	Incomplete     bool                   `json:"incomplete"`
	Reason         string                 `json:"reason,omitempty"`
}

// Builder accumulates records deterministically.
type Builder struct {
	rec RunRecord
}

// NewBuilder starts a record for a case.
func NewBuilder(caseID string, versions, params map[string]string) *Builder {
	return &Builder{rec: RunRecord{
		CaseID: caseID,
		ParserVersions: cloneMap(versions),
		Parameters:     cloneMap(params),
	}}
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// AddChunk records one output chunk with its source lineage.
func (b *Builder) AddChunk(c ChunkRecord) { b.rec.Chunks = append(b.rec.Chunks, c) }

// AddSilence attaches a silence report with its effective parameters.
func (b *Builder) AddSilence(r SilenceRecord) { b.rec.Silence = append(b.rec.Silence, r) }

// AddError records a located failure without dropping prior chunk records.
func (b *Builder) AddError(e model.SourceError) { b.rec.Errors = append(b.rec.Errors, e) }

// Write serializes the record atomically into the journal state tree. The
// file is content-addressed by case id and never mutated after creation.
func (b *Builder) Write(dir string) (string, error) {
	sort.Slice(b.rec.Chunks, func(i, j int) bool {
		if b.rec.Chunks[i].Representation != b.rec.Chunks[j].Representation {
			return b.rec.Chunks[i].Representation < b.rec.Chunks[j].Representation
		}
		return b.rec.Chunks[i].ChunkID < b.rec.Chunks[j].ChunkID
	})
	if err := os.MkdirAll(filepath.Join(dir, "provenance"), 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "provenance", b.rec.CaseID+".json")
	data, err := json.MarshalIndent(b.rec, "", "  ")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".prov-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return path, nil
}

// Record exposes the accumulated bundle for the JSON report.
func (b *Builder) Record() RunRecord { return b.rec }
