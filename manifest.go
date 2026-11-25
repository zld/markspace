package mediakernel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// Manifest schema versions understood by this kernel.
const (
	ManifestV1      = 1
	ManifestV2      = 2
	ManifestCurrent = ManifestV2
)

// LegacyManifestV1Entry is the older V1 asset record: it had no container digest and
// no quality block. It exists only to decode legacy on-disk manifests.
type LegacyManifestV1Entry struct {
	AssetID       string `json:"asset_id"`
	Kind          string `json:"kind"`
	SourceID      string `json:"source_id"`
	SourceDigest  Digest `json:"source_digest"`
	TimelineStart int64  `json:"timeline_start_ticks"`
	TimelineEnd   int64  `json:"timeline_end_ticks"`
}

// ManifestV1 is the on-disk shape of a generation-1 manifest.
type LegacyManifestV1 struct {
	Version int                     `json:"version"`
	Seed    int64                   `json:"seed"`
	Entries []LegacyManifestV1Entry `json:"entries"`
}

// AssetEntry is the current (V2) asset record.
type AssetEntry struct {
	AssetID         string        `json:"asset_id"`
	Kind            string        `json:"kind"`
	SourceID        string        `json:"source_id"`
	ContentDigest   Digest        `json:"content_digest"`
	ContainerDigest Digest        `json:"container_digest"`
	TimelineStart   int64         `json:"timeline_start_ticks"`
	TimelineEnd     int64         `json:"timeline_end_ticks"`
	Quality         QualityResult `json:"quality"`
}

// Manifest is the current on-disk manifest. IndexDigest covers the source
// generation, ManifestDigest covers this exact document, and Stage records the
// migration state so an interrupted run can be detected on restart.
type Manifest struct {
	Version        int          `json:"version"`
	Seed           int64        `json:"seed"`
	Stage          string       `json:"stage"`
	IndexDigest    Digest       `json:"index_digest"`
	ManifestDigest Digest       `json:"manifest_digest"`
	Entries        []AssetEntry `json:"entries"`
}

// Migration stages written into Manifest.Stage.
const (
	StageValidate = "validate"
	StageStage    = "stage"
	StageCommit   = "commit"
	StageDone     = "committed"
	StageRollback = "rolled-back"
)

// DecodeManifest reads either schema version and adapts V1 up to the current
// shape. Unknown versions are rejected with a structured migration error.
func DecodeManifest(raw []byte) (*Manifest, error) {
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, NewError(PhaseMigrate, 0, "manifest is not valid JSON").WithCause(err)
	}
	switch head.Version {
	case ManifestV1:
		var old LegacyManifestV1
		if err := json.Unmarshal(raw, &old); err != nil {
			return nil, NewError(PhaseMigrate, 0, "invalid v1 manifest").WithCause(err)
		}
		return adaptV1(&old), nil
	case ManifestV2:
		var m Manifest
		dec := json.NewDecoder(bytes.NewReader(raw))
		if err := dec.Decode(&m); err != nil {
			return nil, NewError(PhaseMigrate, 0, "invalid v2 manifest").WithCause(err)
		}
		return &m, nil
	default:
		return nil, NewError(PhaseMigrate, 0,
			fmt.Sprintf("unsupported manifest version %d", head.Version))
	}
}

// adaptV1 upgrades a legacy manifest: each entry gains an empty container
// digest (forcing container-dependent rebuilds) and a not-applicable quality
// block, since neither was tracked in V1.
func adaptV1(old *LegacyManifestV1) *Manifest {
	m := &Manifest{Version: ManifestCurrent, Seed: old.Seed, Stage: StageDone}
	for _, e := range old.Entries {
		m.Entries = append(m.Entries, AssetEntry{
			AssetID:       e.AssetID,
			Kind:          e.Kind,
			SourceID:      e.SourceID,
			ContentDigest: e.SourceDigest,
			TimelineStart: e.TimelineStart,
			TimelineEnd:   e.TimelineEnd,
			Quality:       SNRProxy(e.AssetID, false, 0, 0),
		})
	}
	return m
}

// Validate enforces invariants required before a manifest can be committed.
// A time mis-ordering is reported with the offending entry index as offset.
func (m *Manifest) Validate() error {
	if m.Version != ManifestCurrent {
		return NewError(PhaseMigrate, 0,
			fmt.Sprintf("manifest version %d is not the current %d", m.Version, ManifestCurrent))
	}
	seen := make(map[string]bool, len(m.Entries))
	for i, e := range m.Entries {
		if e.AssetID == "" {
			return NewError(PhaseSpec, int64(i), "entry has empty asset_id")
		}
		if seen[e.AssetID] {
			return NewError(PhaseSpec, int64(i), "duplicate asset_id: "+e.AssetID)
		}
		seen[e.AssetID] = true
		if e.TimelineEnd < e.TimelineStart {
			return NewError(PhaseSpec, int64(i),
				"timeline end precedes start for "+e.AssetID).
				WithTime(TimeRange{StartTicks: e.TimelineStart, EndTicks: e.TimelineEnd})
		}
		if e.ContentDigest.Hex == "" {
			return NewError(PhaseDigest, int64(i), "missing content digest for "+e.AssetID)
		}
	}
	return nil
}

// CanonicalBytes renders the document without the self-referential manifest
// digest field, in deterministic compact JSON, for digest computation.
func (m *Manifest) CanonicalBytes() ([]byte, error) {
	type withoutDigest struct {
		Version     int          `json:"version"`
		Seed        int64        `json:"seed"`
		Stage       string       `json:"stage"`
		IndexDigest Digest       `json:"index_digest"`
		Entries     []AssetEntry `json:"entries"`
	}
	doc := withoutDigest{Version: m.Version, Seed: m.Seed, Stage: m.Stage,
		IndexDigest: m.IndexDigest, Entries: m.Entries}
	return json.Marshal(doc)
}

// ComputeDigest fills ManifestDigest from the canonical encoding.
func (m *Manifest) ComputeDigest() error {
	b, err := m.CanonicalBytes()
	if err != nil {
		return NewError(PhaseMigrate, -1, "canonical manifest marshal failed").WithCause(err)
	}
	m.ManifestDigest = DigestBytes(b)
	return nil
}

// LoadManifest reads and decodes a manifest file. A missing file is reported
// with os.ErrNotExist so callers can treat the first run specially.
func LoadManifest(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DecodeManifest(raw)
}
