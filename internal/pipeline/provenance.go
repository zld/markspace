package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"archivist/internal/fault"
	"archivist/internal/index"
	"archivist/internal/repair"
	"archivist/internal/scan"
)

// ProvenanceVersion guards the provenance schema.
const ProvenanceVersion = "provenance/v1"

// RulesVersion combines the clock, repair and index rule identifiers so a
// provenance record states exactly which logic produced the output.
func RulesVersion() string {
	return "clock/v1+" + repair.Version + "+" + index.Version
}

// TrackProvenance records the per-track outcome.
type TrackProvenance struct {
	TrackID         uint32             `json:"track_id"`
	Timescale       uint32             `json:"timescale"`
	SampleCount     int                `json:"sample_count"`
	RepairedCount   int                `json:"repaired_count"`
	KeyframeCount   int                `json:"keyframe_count"`
	IndexEntries    int                `json:"index_entries"`
	FirstPTS        int64              `json:"first_pts"`
	LastPTS         int64              `json:"last_pts"`
	IndexOffsetBase int64              `json:"index_offset_base"`
	Corrections     []CorrectionRecord `json:"corrections,omitempty"`
}

// CorrectionRecord is a compact record of one changed timestamp.
type CorrectionRecord struct {
	Sample   int    `json:"sample"`
	Original int64  `json:"original"`
	Repaired int64  `json:"repaired"`
	Kind     string `json:"kind"`
}

// Provenance is the per-input record that ties input, parameters, rules and
// output bytes together. It is written last and is what an operator checks
// when confirming a repair.
type Provenance struct {
	Version         string   `json:"version"`
	Input           Artifact `json:"input"`
	Output          Artifact `json:"output"`
	Params          Params   `json:"params"`
	ParamsHash      string   `json:"params_hash"`
	RulesVersion    string   `json:"rules_version"`
	RepairedEntries int      `json:"repaired_entries"`
	IndexEntries    int      `json:"index_entries"`
	KeyframeEntries int      `json:"keyframe_entries"`
	CheckpointStage Stage    `json:"checkpoint_stage"`
	// Digest is the path-independent SHA-256 of this record. Two runs over
	// the same bytes with the same parameters produce the same value no
	// matter where the work directory lives.
	Digest   string            `json:"digest"`
	Tracks   []TrackProvenance `json:"tracks"`
	Warnings []string          `json:"warnings,omitempty"`
}

// BuildProvenance assembles the record. The provenance digest is the SHA-256
// of the canonical JSON encoding, so identical inputs and parameters always
// yield an identical provenance digest. The checkpoint stage is deliberately
// excluded: it describes how far a particular run got, not what it produced.
func BuildProvenance(input Artifact, params Params, rules string, rep *repair.Result, ix *index.Index, out Artifact, stage Stage, warnings []string) (*Provenance, error) {
	if rep == nil || ix == nil {
		return nil, fault.New(fault.KindInvariant, fault.StagePipeline, "nil result for provenance")
	}
	p := &Provenance{
		Version:         ProvenanceVersion,
		Input:           input,
		Output:          out,
		Params:          params,
		ParamsHash:      params.Hash(),
		RulesVersion:    rules,
		RepairedEntries: rep.TotalRepaired(),
		IndexEntries:    ix.EntryCount(),
		KeyframeEntries: ix.KeyframeCount(),
		CheckpointStage: stage,
		Warnings:        warnings,
	}
	for _, tr := range rep.Tracks {
		tp := TrackProvenance{
			TrackID:       tr.TrackID,
			Timescale:     tr.Timescale,
			SampleCount:   len(tr.Entries),
			RepairedCount: tr.RepairedCount(),
			FirstPTS:      tr.Entries[0].Repaired,
			LastPTS:       tr.Entries[len(tr.Entries)-1].Repaired,
		}
		if it, ok := ix.Track(tr.TrackID); ok {
			tp.IndexEntries = len(it.Entries)
			tp.IndexOffsetBase = it.OutputBase
			for _, e := range it.Entries {
				if e.Keyframe {
					tp.KeyframeCount++
				}
			}
		}
		for _, e := range tr.Entries {
			if e.Kind == repair.KindUnchanged {
				continue
			}
			tp.Corrections = append(tp.Corrections, CorrectionRecord{
				Sample: e.Sample, Original: e.Original, Repaired: e.Repaired, Kind: e.Kind,
			})
		}
		p.Tracks = append(p.Tracks, tp)
	}
	sum, err := canonicalDigest(p)
	if err != nil {
		return nil, err
	}
	p.Digest = sum
	return p, nil
}

// canonicalDigest hashes a deterministic, location-independent rendering of
// p. Absolute paths are omitted so the same input processed in two different
// work directories yields the same provenance digest.
func canonicalDigest(p *Provenance) (string, error) {
	clone := *p
	clone.Digest = ""
	clone.CheckpointStage = ""
	clone.Input.Path = ""
	clone.Output.Path = ""
	data, err := json.Marshal(&clone)
	if err != nil {
		return "", fault.New(fault.KindInvariant, fault.StagePipeline, "encode provenance").Wrap(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// WriteProvenance serializes p to path atomically and returns its artifact.
func WriteProvenance(p *Provenance, path string) (Artifact, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Artifact{}, fault.New(fault.KindIO, fault.StageWrite, "create provenance dir").In(path).Wrap(err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return Artifact{}, fault.New(fault.KindInvariant, fault.StagePipeline, "encode provenance").Wrap(err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return Artifact{}, fault.New(fault.KindIO, fault.StageWrite, "write provenance").In(tmp).Wrap(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return Artifact{}, fault.New(fault.KindIO, fault.StageWrite, "publish provenance").In(path).Wrap(err)
	}
	sum, size, err := hashFile(path)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: path, Size: size, SHA256: sum}, nil
}

// WriteIndex serializes ix next to the output file.
func WriteIndex(ix *index.Index, path string) (Artifact, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Artifact{}, fault.New(fault.KindIO, fault.StageWrite, "create index dir").In(path).Wrap(err)
	}
	data, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return Artifact{}, fault.New(fault.KindInvariant, fault.StagePipeline, "encode index").Wrap(err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return Artifact{}, fault.New(fault.KindIO, fault.StageWrite, "write index").In(tmp).Wrap(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return Artifact{}, fault.New(fault.KindIO, fault.StageWrite, "publish index").In(path).Wrap(err)
	}
	sum, size, err := hashFile(path)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: path, Size: size, SHA256: sum}, nil
}

// summarizeSkipped renders skipped boxes for provenance warnings.
func summarizeSkipped(c *scan.Container) []string {
	var out []string
	for _, b := range c.Skipped {
		out = append(out, fmt.Sprintf("skipped box %s at %d", b.Type, b.Offset))
	}
	sort.Strings(out)
	return out
}
