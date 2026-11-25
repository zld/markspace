// Package provenance records and independently verifies source and artifact
// hashes and processing history.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/model"
	mp4scan "archivecheck/internal/mp4"
)

// HashFile returns the SHA-256 and size of a local file.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, apperrors.Wrap(apperrors.KindProvenance, err).WithPath(path)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, apperrors.Wrap(apperrors.KindProvenance, err).WithPath(path)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ArtifactSetHash hashes the normalized path/hash/size tuples. It is stable
// regardless of artifact slice order.
func ArtifactSetHash(arts []model.ArtifactSummary) string {
	cp := append([]model.ArtifactSummary(nil), arts...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Path < cp[j].Path })
	h := sha256.New()
	for _, a := range cp {
		_, _ = fmt.Fprintf(h, "%s|%s|%d\n", a.Path, a.SHA256, a.SizeBytes)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FromScan builds the source provenance record.
func FromScan(scan *mp4scan.ScanResult, asset model.InputAsset, layers []model.LayerReport, transitions []model.StageTransition) model.ProvenanceRecord {
	rec := model.ProvenanceRecord{
		SourcePath:         asset.Path,
		SourceSHA256:       scan.SHA256,
		TrackID:            scan.TrackID,
		Timescale:          scan.Timebase.Timescale,
		ContainerStartTick: int64(scan.Timeline.ContainerStart),
		EditZeroPointTick:  int64(scan.Timeline.EditZeroPoint),
		EditIntervals:      append([]model.EditInterval(nil), asset.EditIntervals...),
		Layers:             append([]model.LayerReport(nil), layers...),
		Transitions:        append([]model.StageTransition(nil), transitions...),
	}
	for _, l := range layers {
		rec.Recovery = append(rec.Recovery, l.Recovery...)
	}
	return rec
}

// VerifyArtifacts independently recomputes every artifact hash and size from
// the output manifest. Manifest paths are interpreted relative to outDir.
func VerifyArtifacts(outDir string, m *model.OutputManifest) error {
	for _, expected := range m.Artifacts {
		full := expected.Path
		if !filepath.IsAbs(full) {
			full = filepath.Join(outDir, expected.Path)
		}
		gotHash, size, err := HashFile(full)
		if err != nil {
			return err
		}
		if gotHash != expected.SHA256 || size != expected.SizeBytes {
			return apperrors.E(apperrors.KindProvenance,
				fmt.Sprintf("artifact digest/size mismatch for %s", expected.Path)).WithPath(full)
		}
	}
	if got := ArtifactSetHash(m.Artifacts); !strings.EqualFold(got, m.ArtifactSetSHA256) {
		return apperrors.E(apperrors.KindProvenance, "artifact set hash mismatch")
	}
	return nil
}

// VerifySource recomputes a source hash when the source path is accessible.
func VerifySource(asset model.InputAsset, expectedSHA string) error {
	got, _, err := HashFile(asset.Path)
	if err != nil {
		return err
	}
	if got != expectedSHA {
		return apperrors.E(apperrors.KindProvenance, "source file hash mismatch").
			WithAsset(asset.ID).WithPath(asset.Path)
	}
	return nil
}
