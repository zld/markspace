// Package maptrack resolves explicit source-to-target track bindings,
// adjudicates metadata that conflicts across MXF and MP4 deliveries and
// rejects missing, duplicated or codec-incompatible mappings with a stable
// error code and the offending track number.
package maptrack

import (
	"fmt"
	"sort"

	"mediapack/internal/demux"
	"mediapack/internal/model"
)

// Rule is one explicit source-track to target-track binding. Role, when set,
// must equal the source handler ("vide"/"soun") or the mapping is rejected as
// codec-incompatible.
type Rule struct {
	SourcePath  string
	SourceTrack uint32
	TargetTrack uint32
	Role        string
}

// Decision records how one metadata field was resolved: every candidate's
// raw value, the adopted value and the container that supplied it. Nothing is
// overwritten silently.
type Decision struct {
	Field      string             `json:"field"`
	Candidates []model.Field[any] `json:"candidates"`
	Adopted    model.Field[any]   `json:"adopted"`
}

// Binding is a resolved target track: its single source, adjudicated metadata
// and the samples relabeled with the target track id.
type Binding struct {
	TargetTrack uint32          `json:"targetTrack"`
	SourcePath  string          `json:"sourcePath"`
	SourceTrack uint32          `json:"sourceTrack"`
	Format      string          `json:"format"`
	Codec       string          `json:"codec"`
	Meta        model.TrackMeta `json:"meta"`
	Decisions   []Decision      `json:"decisions"`
	Samples     []model.Sample  `json:"samples"`
}

// Mapping is the validated, deterministic output of the mapper.
type Mapping struct {
	Bindings []*Binding `json:"bindings"`
}

// Defaults supplies fallback metadata when neither container carries a field.
type Defaults struct {
	Rotation model.Rotation
	Crop     model.Crop
	Color    model.Color
}

// StandardDefaults returns the neutral fallback used by the demo and tests.
func StandardDefaults() Defaults {
	return Defaults{
		Rotation: model.Rotation{Degrees: 0},
		Crop:     model.Crop{},
		Color:    model.Color{Primaries: 1, Transfer: 1, Matrix: 1},
	}
}

// Build validates the rules against the demuxed containers and produces a
// mapping. It returns the first locatable error and never a partial result.
func Build(containers []*demux.Container, rules []Rule, def Defaults) (*Mapping, error) {
	if len(rules) == 0 {
		return nil, model.NewError(model.CodeMalformedContainer,
			"no mapping rules supplied")
	}
	index := indexContainers(containers)
	bindings := make([]*Binding, 0, len(rules))
	seenTarget := map[uint32]bool{}
	seenSource := map[string]bool{}

	for _, rule := range rules {
		key := sourceKey(rule.SourcePath, rule.SourceTrack)
		c, ok := index[key]
		if !ok {
			return nil, model.NewError(model.CodeMissingTrack,
				fmt.Sprintf("source track %d not found in %s",
					rule.SourceTrack, rule.SourcePath)).
				WithFile(rule.SourcePath).WithTrack(rule.SourceTrack)
		}
		if seenTarget[rule.TargetTrack] {
			return nil, model.NewError(model.CodeDuplicateTarget,
				fmt.Sprintf("target track %d bound more than once",
					rule.TargetTrack)).
				WithFile(rule.SourcePath).WithTrack(rule.TargetTrack)
		}
		if seenSource[key] {
			return nil, model.NewError(model.CodeDuplicateTarget,
				"source track mapped twice").
				WithFile(rule.SourcePath).WithTrack(rule.SourceTrack)
		}
		meta := c.TrackMeta(rule.SourceTrack)
		if rule.Role != "" && meta.Handler != rule.Role {
			return nil, model.NewError(model.CodeIncompatibleCodec,
				fmt.Sprintf("codec %s/handler %s cannot serve role %s",
					meta.Codec, meta.Handler, rule.Role)).
				WithFile(rule.SourcePath).WithTrack(rule.SourceTrack)
		}
		if meta.Codec == "" {
			return nil, model.NewError(model.CodeIncompatibleCodec,
				"source track has no codec identifier").
				WithFile(rule.SourcePath).WithTrack(rule.SourceTrack)
		}
		adjudicated, decisions := adjudicate(containers, rule.SourceTrack, def)
		// Keep the bound source's own identity fields; only presentation
		// metadata comes from the cross-container adjudication above.
		adjudicated.TrackID = meta.TrackID
		adjudicated.Timescale = meta.Timescale
		adjudicated.Handler = meta.Handler
		adjudicated.Codec = meta.Codec
		samples := relabel(c.Samples[rule.SourceTrack], rule.TargetTrack)
		bindings = append(bindings, &Binding{
			TargetTrack: rule.TargetTrack,
			SourcePath:  rule.SourcePath,
			SourceTrack: rule.SourceTrack,
			Format:      c.Format,
			Codec:       meta.Codec,
			Meta:        adjudicated,
			Decisions:   decisions,
			Samples:     samples,
		})
		seenTarget[rule.TargetTrack] = true
		seenSource[key] = true
	}
	sort.Slice(bindings, func(i, j int) bool {
		return bindings[i].TargetTrack < bindings[j].TargetTrack
	})
	return &Mapping{Bindings: bindings}, nil
}

func sourceKey(path string, track uint32) string {
	return fmt.Sprintf("%s#%d", path, track)
}

func indexContainers(cs []*demux.Container) map[string]*demux.Container {
	idx := map[string]*demux.Container{}
	for _, c := range cs {
		for _, t := range c.Tracks {
			idx[sourceKey(c.Path, t.TrackID)] = c
		}
	}
	return idx
}

func relabel(in []model.Sample, target uint32) []model.Sample {
	out := make([]model.Sample, len(in))
	for i, s := range in {
		s.TargetTrack = target
		out[i] = s
	}
	return out
}
