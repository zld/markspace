package pipeline

import (
	"errors"
	"os"

	"mediapack/model"
	"mediapack/segment"
)

func writeOrResume(root string, cfg Config, m *segment.Manifest, files map[string][]byte) (*ResumeInfo, error) {
	if err := m.FillChecksum(); err != nil {
		return nil, err
	}
	if !cfg.Resume {
		if err := writeAllAt(root, m, files); err != nil {
			return nil, err
		}
		return nil, nil
	}

	info := &ResumeInfo{Attempted: true, ManifestSHA256: m.ManifestSHA256}
	existing, _, err := segment.ReadManifest(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := writeAllAt(root, m, files); err != nil {
				return nil, err
			}
			info.ExistingManifest = false
			info.ContinuedSegments = len(files)
			return info, nil
		}
		return nil, err
	}

	info.ExistingManifest = true
	if existing.ManifestSHA256 != m.ManifestSHA256 {
		return info, model.NewError("", "manifest.json", -1, model.CategoryUnsafeResume,
			"existing manifest summary does not match regenerated input", nil)
	}
	verification, err := segment.VerifyAllSegments(root, m)
	if err != nil {
		return info, err
	}
	info.VerifiedSegments = verification.VerifiedSegments
	for _, ref := range verification.MissingSegments {
		data, ok := files[ref.Path]
		if !ok {
			return info, model.NewError(model.TrackID(ref.Track), ref.Path, -1,
				model.CategoryOutputWrite, "cannot regenerate missing segment", nil)
		}
		if _, err := segment.WriteFileSafe(root, ref.Path, data); err != nil {
			return info, err
		}
		info.ContinuedSegments++
	}
	if _, err := segment.WriteManifest(root, m); err != nil {
		return info, err
	}
	return info, nil
}

func writeAllAt(root string, m *segment.Manifest, files map[string][]byte) error {
	for _, rep := range m.Representations {
		for _, ref := range rep.Segments {
			data := files[ref.Path]
			if _, err := segment.WriteFileSafe(root, ref.Path, data); err != nil {
				return err
			}
		}
	}
	_, err := segment.WriteManifest(root, m)
	return err
}
