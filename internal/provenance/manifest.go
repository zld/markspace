// Package provenance builds the verifiable source manifest: every source
// file, node output and gap is recorded with byte offsets and SHA-256
// digests, and the final delivery summary can be re-verified independently.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"pwf/internal/dag"
	"pwf/internal/ingest"
	"pwf/internal/media"
)

// SourceEntry describes one input file.
type SourceEntry struct {
	Track  string `json:"track"`
	Path   string `json:"path"`
	Codec  string `json:"codec"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// OffsetEntry pins one parsed packet or sample to its file position.
type OffsetEntry struct {
	Track    string `json:"track"`
	Sequence uint64 `json:"sequence,omitempty"`
	Offset   int64  `json:"offset"`
	Bytes    int    `json:"bytes"`
}

// GapEntry records a missing sequence range without inventing audio.
type GapEntry struct {
	Track    string `json:"track"`
	StartSeq uint64 `json:"start_seq"`
	Count    uint64 `json:"count"`
}

// NodeEntry records one DAG node and its committed output digest.
type NodeEntry struct {
	NodeKey string `json:"node_key"`
	Status  string `json:"status"`
	SHA256  string `json:"sha256"`
	Reused  bool   `json:"reused"`
}

// Manifest is the full verifiable delivery document.
type Manifest struct {
	SchemaVersion string        `json:"schema_version"`
	Sources       []SourceEntry `json:"sources"`
	Offsets       []OffsetEntry `json:"offsets"`
	Gaps          []GapEntry    `json:"gaps"`
	Nodes         []NodeEntry   `json:"nodes"`
	ConfigReport  string        `json:"config_report"`
	RunSHA256     string        `json:"run_sha256"` // digest of the full-run manifest body
}

// Builder accumulates manifest entries deterministically.
type Builder struct {
	m Manifest
}

// NewBuilder starts a manifest for the given schema version.
func NewBuilder() *Builder {
	return &Builder{m: Manifest{SchemaVersion: "pwf-manifest-v1"}}
}

// AddSource hashes an input file and records it.
func (b *Builder) AddSource(track, path, codec string) error {
	sum, size, err := hashFile(path)
	if err != nil {
		return media.NewError(media.KindCorrupt, media.StageProvenance,
			"cannot hash source "+path, err).WithTrack(track)
	}
	b.m.Sources = append(b.m.Sources, SourceEntry{
		Track: track, Path: filepath.Base(path), Codec: codec, SHA256: sum, Bytes: size,
	})
	return nil
}

// AddRTPStream records packet offsets and gaps from a parsed RTP result.
func (b *Builder) AddRTPStream(res *ingest.RTPStreamResult) {
	for _, rec := range res.Records {
		b.m.Offsets = append(b.m.Offsets, OffsetEntry{
			Track: rec.Track, Sequence: rec.ExtSequence,
			Offset: rec.Offset, Bytes: rec.PacketBytes,
		})
	}
	for _, g := range res.Gaps {
		b.m.Gaps = append(b.m.Gaps, GapEntry{
			Track: g.Track, StartSeq: g.StartSeq, Count: g.Count,
		})
	}
}

// AddMP4Track records sample offsets from a parsed MP4 result.
func (b *Builder) AddMP4Track(res *ingest.MP4TrackResult) {
	for _, s := range res.Samples {
		b.m.Offsets = append(b.m.Offsets, OffsetEntry{
			Track: res.Track, Sequence: uint64(s.Index),
			Offset: s.Offset, Bytes: int(s.Size),
		})
	}
}

// AddNodes records DAG execution results in topological order.
func (b *Builder) AddNodes(results []dag.RunResult) {
	for _, r := range results {
		b.m.Nodes = append(b.m.Nodes, NodeEntry{
			NodeKey: r.Key, Status: string(r.Status), SHA256: r.SHA256, Reused: r.Reused,
		})
	}
}

// SetConfigReport attaches the planner's per-track configuration report.
func (b *Builder) SetConfigReport(report string) { b.m.ConfigReport = report }

// canonicalBody returns the deterministic JSON bytes the digest is computed
// over. json.Marshal sorts map keys and struct field order is fixed, so the
// body is reproducible byte-for-byte on every machine and run.
func canonicalBody(m Manifest) ([]byte, error) {
	m.RunSHA256 = ""
	return json.Marshal(m)
}

// Build finalizes the manifest and computes its whole-body digest.
func (b *Builder) Build() (*Manifest, error) {
	body, err := canonicalBody(b.m)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	b.m.RunSHA256 = hex.EncodeToString(sum[:])
	return &b.m, nil
}

// Write serializes the manifest to path atomically and returns its digest.
func (m *Manifest) Write(path string) (string, error) {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return m.RunSHA256, nil
}

// Verify reloads a manifest file and recomputes its canonical body digest,
// returning an error on any mismatch. It also re-hashes every source listed.
func Verify(path, sourceDir string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, media.NewError(media.KindVerify, media.StageProvenance,
			"manifest unreadable", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, media.NewError(media.KindVerify, media.StageProvenance,
			"manifest malformed", err)
	}
	stored := m.RunSHA256
	body, err := canonicalBody(m)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != stored {
		return nil, media.NewError(media.KindVerify, media.StageProvenance,
			fmt.Sprintf("manifest digest mismatch: stored=%s recomputed=%s", stored, hex.EncodeToString(sum[:])), nil)
	}
	for _, src := range m.Sources {
		sum, _, err := hashFile(filepath.Join(sourceDir, src.Path))
		if err != nil {
			return nil, media.NewError(media.KindVerify, media.StageProvenance,
				"source missing for re-hash: "+src.Path, err)
		}
		if sum != src.SHA256 {
			return nil, media.NewError(media.KindVerify, media.StageProvenance,
				fmt.Sprintf("source %s digest changed", src.Path), nil)
		}
	}
	return &m, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := copyBuffered(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
