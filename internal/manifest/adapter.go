// Package manifest: the version adapter.
//
// Every read of a legacy manifest goes through this file.  The rest of the
// pipeline only ever sees V2.  Conversion preserves the source version, the
// source digest and the media intervals of each asset, and refuses to build a
// new manifest when a source digest disagrees with the asset that was
// actually produced.
package manifest

import (
	"bytes"
	"encoding/json"
	"io"

	"example.com/assetpack/internal/fault"
)

// Version numbers understood by the adapter.
const (
	VersionLegacy = 1
	VersionV2     = 2
)

// Adapter converts legacy manifests and merges manifests idempotently.
type Adapter struct {
	Release string
	Target  TargetSpec
}

// Resolved carries what the audio stage actually produced for one legacy
// asset.  SourceSHA256 must match the digest recorded in the legacy manifest.
type Resolved struct {
	Name         string
	SHA256       string
	SourceSHA256 string
	Bytes        int64
	Artifact     string
	Frames       int64
	LoudnessLUFS float64
	Peak         float64
	GapSamples   int64
	Media        []Interval
}

// ReadFile reads either manifest version, returning the parsed V2 for v2
// input and the raw V1 plus a converted V2 skeleton for v1 input.
func (a *Adapter) ReadFile(r io.Reader) (legacy *V1, current *V2, err error) {
	raw, err := io.ReadAll(io.LimitReader(r, 32<<20))
	if err != nil {
		return nil, nil, fault.New(fault.StageManifest, fault.KindIO, "read manifest: %v", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil, fault.New(fault.StageManifest, fault.KindMissing, "manifest file is empty")
	}
	var probe struct {
		Schema  *string `json:"schema"`
		Version *int    `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, nil, fault.New(fault.StageManifest, fault.KindMissing, "manifest is not valid JSON: %v", err)
	}
	switch {
	case probe.Schema != nil:
		m, err := ReadV2(bytes.NewReader(raw))
		if err != nil {
			return nil, nil, err
		}
		return nil, m, nil
	case probe.Version != nil && *probe.Version == VersionLegacy:
		m, err := ReadV1(bytes.NewReader(raw))
		if err != nil {
			return nil, nil, err
		}
		return m, nil, nil
	case probe.Version != nil:
		return nil, nil, fault.New(fault.StageManifest, fault.KindUnknownVer,
			"manifest version %d is not supported (known: 1, %q)", *probe.Version, SchemaV2)
	default:
		return nil, nil, fault.New(fault.StageManifest, fault.KindMissing,
			"manifest names neither a schema nor a version")
	}
}

// Convert builds a V2 manifest from a legacy manifest plus the resolved
// outputs.  The result is not committed here; callers stage and verify it.
func (a *Adapter) Convert(old *V1, resolved []Resolved) (*V2, error) {
	if old == nil {
		return nil, fault.New(fault.StageManifest, fault.KindMissing, "no legacy manifest to convert")
	}
	if a.Release == "" {
		return nil, fault.New(fault.StageManifest, fault.KindMissing, "adapter has no release name")
	}
	byName := make(map[string]Resolved, len(resolved))
	for _, r := range resolved {
		if _, dup := byName[r.Name]; dup {
			return nil, fault.New(fault.StageManifest, fault.KindInvalidArg,
				"output %q resolved twice", r.Name)
		}
		byName[r.Name] = r
	}
	m := &V2{Schema: SchemaV2, Release: a.Release, Sequence: 1, Target: a.Target}
	for i, asset := range old.Assets {
		r, ok := byName[asset.Name]
		if !ok {
			return nil, fault.New(fault.StageManifest, fault.KindMissing,
				"legacy asset %q was not produced", asset.Name).AtSample(int64(i))
		}
		if r.SourceSHA256 != "" && r.SourceSHA256 != asset.SHA256 {
			return nil, fault.New(fault.StageManifest, fault.KindDigest,
				"asset %q source digest changed: legacy %s, produced %s",
				asset.Name, asset.SHA256, r.SourceSHA256).AtSample(int64(i))
		}
		if r.SHA256 == "" || r.Artifact == "" || r.Bytes <= 0 {
			return nil, fault.New(fault.StageManifest, fault.KindMissing,
				"asset %q has an incomplete resolved output", asset.Name).AtSample(int64(i))
		}
		media := append([]Interval(nil), r.Media...)
		if len(media) == 0 {
			media = []Interval{{StartSample: 0, LengthSamples: r.Frames, SourceOffset: 0}}
		}
		m.Resources = append(m.Resources, Resource{
			Name:          asset.Name,
			Kind:          "audio",
			SourceVersion: VersionLegacy,
			SourceSHA256:  asset.SHA256,
			SHA256:        r.SHA256,
			Bytes:         r.Bytes,
			Artifact:      r.Artifact,
			Rate:          a.Target.Rate,
			Layout:        a.Target.Layout,
			Frames:        r.Frames,
			LoudnessLUFS:  r.LoudnessLUFS,
			Peak:          r.Peak,
			GapSamples:    r.GapSamples,
			Media:         media,
		})
	}
	if len(m.Resources) != len(old.Assets) {
		return nil, fault.New(fault.StageManifest, fault.KindMissing,
			"conversion produced %d of %d legacy assets", len(m.Resources), len(old.Assets))
	}
	if _, err := m.ComputeDigest(); err != nil {
		return nil, err
	}
	return m, nil
}
