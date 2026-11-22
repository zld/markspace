package segmenter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"archivist/internal/model"
)

// Manifest is the published multi-bitrate description of one asset.
type Manifest struct {
	SchemaVersion   int                    `json:"schema_version"`
	AssetName       string                 `json:"asset_name"`
	CommonTimescale uint64                 `json:"common_timescale"`
	SegmentMS       uint32                 `json:"segment_ms"`
	Representations []ManifestRepresentation `json:"representations"`
	Decisions       []model.Decision       `json:"decisions"`
	Warnings        []model.Warning        `json:"warnings"`
	Digest          string                 `json:"digest"`
}

// ManifestRepresentation groups one bitrate layer's segment entries.
type ManifestRepresentation struct {
	Label    string            `json:"label"`
	Bitrate  uint32            `json:"bitrate"`
	TrackID  uint32            `json:"track_id"`
	Segments []ManifestSegment `json:"segments"`
}

// ManifestSegment is the verifiable per-segment record.
type ManifestSegment struct {
	Index       int         `json:"index"`
	StartDTS    uint64      `json:"start_dts"`
	EndDTS      uint64      `json:"end_dts"`
	ByteRanges  []ByteRange `json:"byte_ranges"`
	SampleCount int         `json:"sample_count"`
	SHA256      string      `json:"sha256"`
}

// BuildManifest assembles the JSON-ready manifest from a cut plan. It does not
// touch the filesystem; call Publish to write atomically.
func BuildManifest(
	name string,
	plan *CutPlan,
	reps []Representation,
	decisions []model.Decision,
	warnings []model.Warning,
) (*Manifest, error) {
	m := &Manifest{
		SchemaVersion:   1,
		AssetName:       name,
		CommonTimescale: plan.CommonTimescale,
		SegmentMS:       plan.SegmentMS,
		Decisions:       decisions,
		Warnings:        warnings,
	}
	for _, rep := range reps {
		segs := plan.Segments[rep.TrackID]
		mr := ManifestRepresentation{Label: rep.Label, Bitrate: rep.Bitrate, TrackID: rep.TrackID}
		for _, s := range segs {
			mr.Segments = append(mr.Segments, ManifestSegment{
				Index:       s.Index,
				StartDTS:    s.StartDTS,
				EndDTS:      s.EndDTS,
				ByteRanges:  s.ByteRanges,
				SampleCount: s.SampleCount,
				SHA256:      s.Hash,
			})
		}
		m.Representations = append(m.Representations, mr)
	}
	digest, err := contentDigest(m)
	if err != nil {
		return nil, err
	}
	m.Digest = digest
	return m, nil
}

// Publish writes the manifest atomically: temp file + fsync + rename, so a
// crash or blocked publication can never leave a half-written manifest.
func Publish(m *Manifest, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	final := filepath.Join(dir, "manifest.json")
	tmp := final + ".tmp"
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return final, nil
}

// Reject removes any staging artifact and returns a blocking error carrying
// the supplied reason. It guarantees no partial manifest remains on disk.
func Reject(dir, reason string) error {
	_ = os.Remove(filepath.Join(dir, "manifest.json.tmp"))
	_ = os.Remove(filepath.Join(dir, "manifest.json"))
	return model.AtTrack(model.StageManifest, model.ErrManifestBlocked, 0, reason)
}

// VerifyHashes recomputes every segment hash from the source bytes and
// compares it against the manifest. The returned ratio is match/total.
func VerifyHashes(m *Manifest, data []byte) (matched, total int, err error) {
	for _, rep := range m.Representations {
		for _, seg := range rep.Segments {
			total++
			h := sha256.New()
			for _, br := range seg.ByteRanges {
				end := br.Offset + br.Length
				if end > uint64(len(data)) {
					return matched, total, model.AtTrack(model.StageManifest,
						model.ErrOutOfBoundsOffset, rep.TrackID,
						fmt.Sprintf("segment %d range past EOF", seg.Index))
				}
				h.Write(data[br.Offset:end])
			}
			if hex.EncodeToString(h.Sum(nil)) == seg.SHA256 {
				matched++
			}
		}
	}
	return matched, total, nil
}

// contentDigest hashes the manifest excluding its own Digest field, with map
// keys ordered so the result is deterministic across runs.
func contentDigest(m *Manifest) (string, error) {
	cp := *m
	cp.Digest = ""
	// Sort decisions/warnings by stable keys before encoding.
	sort.SliceStable(cp.Decisions, func(i, j int) bool {
		return cp.Decisions[i].Scope+cp.Decisions[i].Field <
			cp.Decisions[j].Scope+cp.Decisions[j].Field
	})
	data, err := json.Marshal(cp)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
