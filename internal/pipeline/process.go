package pipeline

import (
	"errors"
	"os"
	"path/filepath"

	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/model"
	mp4 "archivecheck/internal/mp4"
	"archivecheck/internal/plan"
	"archivecheck/internal/provenance"
	"archivecheck/internal/segment"
	"archivecheck/internal/store"
)

func (p *processor) process() (*Result, error) {
	if p.j.Stage == plan.StagePendingScan {
		for _, asset := range p.input.Assets {
			scan, err := mp4.ScanAsset(asset)
			if err != nil {
				return nil, p.fail(err)
			}
			p.j.Scans[asset.ID] = *scan
		}
		if err := p.advance(plan.StageScanned, "container and edit-list scan complete"); err != nil {
			if errors.Is(err, errInterrupt) {
				return nil, err
			}
			return nil, p.fail(err)
		}
	}

	if p.j.Stage == plan.StageScanned {
		if err := p.advance(plan.StagePlanned, "deterministic stage and fragment plan"); err != nil {
			if errors.Is(err, errInterrupt) {
				return nil, err
			}
			return nil, p.fail(err)
		}
	}
	if p.j.Stage == plan.StagePlanned {
		if err := p.advance(plan.StageProcessing, "layer cursors opened"); err != nil {
			if errors.Is(err, errInterrupt) {
				return nil, err
			}
			return nil, p.fail(err)
		}
	}

	var assetReports []model.AssetReport
	var allArtifacts []model.ArtifactSummary
	maxBoundary := int64(0)

	for _, asset := range p.input.Assets {
		scanValue, ok := p.j.Scans[asset.ID]
		if !ok {
			scan, err := mp4.ScanAsset(asset)
			if err != nil {
				return nil, p.fail(err)
			}
			scanValue = *scan
			p.j.Scans[asset.ID] = scanValue
		}
		scan := scanValue
		if len(asset.EditIntervals) == 0 {
			err := apperrors.E(apperrors.KindManifest, "asset has no edit intervals").
				WithStage("plan").WithAsset(asset.ID)
			return nil, p.fail(err)
		}

		var layers []model.LayerReport
		var assetArtifacts []model.ArtifactSummary
		var assetMax int64
		for _, bitrate := range p.j.Parameters.Bitrates {
			build, err := segment.BuildLayer(asset.ID, p.recordID, bitrate, &scan, asset.EditIntervals[0])
			if err != nil {
				return nil, p.fail(err)
			}
			layerState := p.j.Layer(bitrate)
			for _, frag := range build.Fragments {
				full := filepath.Join(p.cfg.OutputDir, frag.Summary.Path)
				if frag.Index < layerState.Cursor {
					if layerState.FragmentSHA256[frag.Index] != frag.Hash {
						err := apperrors.E(apperrors.KindLayer, "committed fragment hash differs on rebuild").
							WithBitrate(bitrate).WithAsset(asset.ID)
						return nil, p.fail(err)
					}
					if _, _, err := provenance.HashFile(full); err != nil {
						return nil, p.fail(err)
					}
				} else {
					if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
						return nil, p.fail(apperrors.Wrap(apperrors.KindIO, err).WithPath(full))
					}
					if err := store.WriteFileAtomic(full, frag.Bytes, 0o644); err != nil {
						return nil, p.fail(err)
					}
					if err := layerState.RecordFragment(frag.Index, frag.Hash); err != nil {
						return nil, p.fail(err)
					}
					p.j.AddArtifact(frag.Summary)
					if err := p.j.SaveAtomic(p.journalPath); err != nil {
						return nil, p.fail(err)
					}
				}
				assetArtifacts = append(assetArtifacts, frag.Summary)
				allArtifacts = append(allArtifacts, frag.Summary)
			}
			layerState.Verified = true
			_ = p.j.SaveAtomic(p.journalPath)
			layers = append(layers, build.Report)
		}

		maxDiff, err := segment.CheckLayerBoundaries(layers)
		if err != nil {
			return nil, p.fail(err)
		}
		assetMax = int64(maxDiff)
		if assetMax > maxBoundary {
			maxBoundary = assetMax
		}
		prov := provenance.FromScan(&scan, asset, layers, p.j.Transitions)
		assetReports = append(assetReports, model.AssetReport{
			AssetID:              asset.ID,
			Status:               string(plan.StageVerified),
			Timeline:             timelineSummary(scan),
			Layers:               layers,
			Artifacts:            assetArtifacts,
			MaxBoundaryDiffTicks: assetMax,
			Provenance:           prov,
		})
	}

	if p.j.Stage == plan.StageProcessing {
		if err := p.advance(plan.StageVerified, "RTP and shared-clock boundary checks passed"); err != nil {
			if errors.Is(err, errInterrupt) {
				return nil, err
			}
			return nil, p.fail(err)
		}
	}

	m := model.OutputManifest{
		SchemaVersion:        model.ManifestSchema,
		StageVersion:         model.StageVersion,
		RecordID:             p.recordID,
		InputSHA256:          p.inputSHA,
		Parameters:           p.j.Parameters,
		Status:               string(plan.StageVerified),
		TransitionCount:      len(p.j.Transitions),
		StageTransitions:     append([]model.StageTransition(nil), p.j.Transitions...),
		Assets:               assetReports,
		Artifacts:            allArtifacts,
		ArtifactSetSHA256:    provenance.ArtifactSetHash(allArtifacts),
		MaxBoundaryDiffTicks: maxBoundary,
	}
	if err := provenance.VerifyArtifacts(p.cfg.OutputDir, &m); err != nil {
		return nil, p.fail(err)
	}
	for _, asset := range p.input.Assets {
		if err := provenance.VerifySource(asset, p.j.Scans[asset.ID].SHA256); err != nil {
			return nil, p.fail(err)
		}
	}
	if err := p.advance(plan.StageCompleted, "manifest and provenance committed"); err != nil {
		if errors.Is(err, errInterrupt) {
			return nil, err
		}
		return nil, p.fail(err)
	}
	m.Status = string(plan.StageCompleted)
	m.StageTransitions = append([]model.StageTransition(nil), p.j.Transitions...)
	m.TransitionCount = len(m.StageTransitions)
	for i := range m.Assets {
		m.Assets[i].Status = string(plan.StageCompleted)
		m.Assets[i].Provenance.Transitions = m.StageTransitions
	}
	p.j.Result = &m
	if err := p.j.SaveAtomic(p.journalPath); err != nil {
		return nil, p.fail(err)
	}
	return p.writeResult(&m)
}

func timelineSummary(s mp4.ScanResult) model.TimelineSummary {
	return model.TimelineSummary{
		Timescale:             s.Timebase.Timescale,
		ContainerStartTick:    int64(s.Timeline.ContainerStart),
		EditZeroPointTick:     int64(s.Timeline.EditZeroPoint),
		EditedStartTick:       int64(s.Timeline.EditedStart),
		EditedDurationTick:    int64(s.Timeline.EditedDuration),
		NonZeroStartPreserved: s.Timeline.NonZeroStartPreserved,
	}
}
