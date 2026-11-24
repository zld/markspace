package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediad/audio"
	"mediad/fingerprint"
	"mediad/internal/clock"
	"mediad/internal/sample"
	"mediad/manifest"
	"mediad/pipeline"
	"mediad/slice"
	"mediad/ts"
)

func buildManifest(kind sample.Kind, cfg Config, work string, sc *scratch, nodes []pipeline.Node) (string, error) {
	demux := sc.demux
	if demux == nil {
		if err := loadArtifact(filepath.Join(work, "ingest.json"), &demux); err != nil {
			return "", err
		}
	}
	if sc.slices == nil {
		var an struct {
			Slices   []slice.Slice    `json:"slices"`
			Silences []audio.Interval `json:"silences"`
		}
		if err := loadArtifact(filepath.Join(work, "analyze.json"), &an); err != nil {
			return "", err
		}
		sc.slices, sc.silences = an.Slices, an.Silences
	}
	if sc.printer == nil {
		if err := loadArtifact(filepath.Join(work, "fingerprint.json"), &sc.printer); err != nil {
			return "", err
		}
	}
	m := manifest.New()
	m.Inputs = []manifest.InputRecord{{
		Name: string(kind), SizeBytes: demux.InputBytes,
		ContentHash: hashBytes(mustInput(kind)),
	}}
	for _, s := range sc.slices {
		m.Slices = append(m.Slices, manifest.SliceRecord{
			TrackID: s.TrackID, StartTicks: s.Start, EndTicks: s.End,
			StartMs: clock.Ms(s.Start), EndMs: clock.Ms(s.End), Bytes: s.Bytes,
			TargetBitrateBPS: s.TargetBitrateBPS, ActualBitrateBPS: s.ActualBitrateBPS,
		})
	}
	for _, fp := range sc.printer {
		m.Fingerprints = append(m.Fingerprints, manifest.FingerprintRecord{
			TrackID: fp.TrackID, WindowIndex: fp.WindowIndex,
			ContentHash: fp.ContentHash, Reused: fp.Reused,
		})
	}
	for _, si := range sc.silences {
		m.Silences = append(m.Silences, manifest.SilenceRecord{
			TrackID: si.TrackID, StartMs: si.StartMs(), EndMs: si.EndMs(),
		})
	}
	m.Errors = demux.Errors
	// Only the manifest's own dependencies are inlined: the manifest node's
	// checkpoint cannot exist before this document is written, so including
	// it would make the document differ between a first run and a resume.
	cps, err := loadDependencyCheckpoints(work, nodes, []string{"ingest", "analyze", "fingerprint"})
	if err != nil {
		return "", err
	}
	m.Checkpoints = cps
	stats := computeStats(demux, sc, cfg)
	// Reused fingerprints within the content windows are a deduplication
	// signal independent of whole-node checkpoint reuse.
	stats.CheckpointReused = reusedFingerprints(sc.printer)
	m.Stats = stats
	sc.manifest = m

	path := filepath.Join(work, "manifest.mp4")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := m.Write(f); err != nil {
		return "", err
	}
	docHash, err := m.DocumentHash()
	if err != nil {
		return "", err
	}
	return docHash, nil
}

func computeStats(demux *ts.DemuxResult, sc *scratch, cfg Config) manifest.Stats {
	var maxDelta int64
	var actualSum int64
	for _, s := range sc.slices {
		if d := s.BoundaryErrorMs(); d > maxDelta {
			maxDelta = d
		}
		actualSum += s.ActualBitrateBPS
	}
	var actual int64
	if len(sc.slices) > 0 {
		actual = actualSum / int64(len(sc.slices))
	}
	return manifest.Stats{
		BitrateTargetBPS: cfg.TargetBitrate,
		BitrateActualBPS: actual,
		QualityErrors:    len(demux.Errors),
		ManifestMaxDelta: maxDelta,
		DuplicateCount:   demux.Duplicates,
	}
}

func writeArtifact(path string, v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return hashBytes(b), nil
}

func loadArtifact(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("app: missing artifact %s (resume cannot reload): %w", path, err)
	}
	return json.Unmarshal(b, v)
}

// loadDependencyCheckpoints returns the persisted records of the named
// dependency nodes, resolved by their idempotency keys and sorted by name so
// the manifest content is deterministic across first runs and resumes.
func loadDependencyCheckpoints(work string, nodes []pipeline.Node, wantNames []string) ([]pipeline.Checkpoint, error) {
	dir := filepath.Join(work, "checkpoints")
	byName := map[string]pipeline.Node{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	var out []pipeline.Checkpoint
	for _, name := range wantNames {
		n, ok := byName[name]
		if !ok {
			continue
		}
		key, err := n.Key()
		if err != nil {
			return nil, err
		}
		var cp pipeline.Checkpoint
		if err := loadArtifact(filepath.Join(dir, key+".json"), &cp); err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, nil
}

func mustInput(kind sample.Kind) []byte {
	data, _ := sample.Generate(kind)
	return data
}

func reusedFingerprints(fps []fingerprint.Fingerprint) int {
	n := 0
	for _, fp := range fps {
		if fp.Reused {
			n++
		}
	}
	return n
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
