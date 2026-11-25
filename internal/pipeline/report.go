package pipeline

import (
	"encoding/json"
	"os"

	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/model"
	"archivecheck/internal/provenance"
	"archivecheck/internal/recovery"
	"archivecheck/internal/store"
)

func (p *processor) completed() (*Result, error) {
	m := p.j.Result
	if m == nil {
		return nil, apperrors.E(apperrors.KindJournal, "completed journal is missing result manifest").
			WithPath(p.journalPath)
	}
	if err := provenance.VerifyArtifacts(p.cfg.OutputDir, m); err != nil {
		return nil, err
	}
	for _, asset := range p.input.Assets {
		if scan, ok := p.j.Scans[asset.ID]; ok {
			if err := provenance.VerifySource(asset, scan.SHA256); err != nil {
				return nil, err
			}
		}
	}
	return p.writeResult(m)
}

func (p *processor) writeResult(m *model.OutputManifest) (*Result, error) {
	b, err := model.CanonicalJSON(m)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindManifest, err)
	}
	manifestSHA := model.SHA256Bytes(b)

	res := &Result{
		Manifest:       *m,
		ManifestBytes:  b,
		ManifestSHA256: manifestSHA,
		ManifestPath:   p.rootPath,
		JournalPath:    p.journalPath,
		Resume:         recovery.FromJournal(p.j),
	}
	if old, err := os.ReadFile(p.rootPath); err == nil {
		res.PreviousManifestSHA256 = model.SHA256Bytes(old)
		res.RepeatManifestByteDiff = byteDiffCount(old, b)
		var oldManifest model.OutputManifest
		if json.Unmarshal(old, &oldManifest) == nil {
			if oldManifest.ArtifactSetSHA256 != m.ArtifactSetSHA256 {
				res.RepeatArtifactDigestDiff = 1
			}
		} else {
			res.RepeatArtifactDigestDiff = 1
		}
	}

	if err := os.MkdirAll(p.cfg.OutputDir, 0o755); err != nil {
		return nil, apperrors.Wrap(apperrors.KindIO, err).WithPath(p.cfg.OutputDir)
	}
	if err := store.WriteFileAtomic(p.manifestPath, b, 0o644); err != nil {
		return nil, err
	}
	if err := store.WriteFileAtomic(p.rootPath, b, 0o644); err != nil {
		return nil, err
	}
	return res, nil
}

func byteDiffCount(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	diff := 0
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			diff++
		}
	}
	if len(a) != len(b) {
		diff += len(a) - n
		if diff < 0 {
			diff = -diff
		}
	}
	return diff
}
