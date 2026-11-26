package pipeline

import (
	"os"

	"replayctl/internal/fault"
	"replayctl/internal/journal"
	mp4scan "replayctl/internal/mp4"
	"replayctl/internal/render"
	"replayctl/internal/validate"
)

func publish(in Input, key, eventID string, art *render.Artifact) (*Result, error) {
	store, err := journal.Open(in.Out)
	if err != nil {
		return nil, err
	}
	final, err := store.FinalPath(key)
	if err != nil {
		return nil, err
	}
	records, err := store.Load()
	if err != nil {
		return nil, err
	}
	rel := artifactRelative(key)

	if rec, ok := records[key]; ok {
		hash, err := validate.HashFile(final)
		if err != nil {
			return nil, err
		}
		if hash != rec.SHA256 {
			return nil, fault.New(fault.Validation, fault.StageJournal, final, -1, -1, "committed artifact hash mismatch")
		}
		metrics, err := measureDelivery(final, art)
		if err != nil {
			return nil, err
		}
		return &Result{
			DeliveryKey: key, Artifact: rel, SHA256: hash, Metrics: metrics,
			Recovered: 0, Published: 1, Duplicate: 0, Committed: true,
		}, nil
	}

	tmp, err := store.CreateTemp(key)
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		return nil, fault.New(fault.IO, fault.StageJournal, tmpName, -1, -1, "close temp: %v", err)
	}
	defer os.Remove(tmpName)

	tracks := []mp4scan.TrackIn{art.Video, art.Audio}
	if err := mp4scan.WriteFile(tmpName, tracks); err != nil {
		return nil, err
	}
	hash, err := validate.HashFile(tmpName)
	if err != nil {
		return nil, err
	}
	metrics, err := measureDelivery(tmpName, art)
	if err != nil {
		return nil, err
	}
	if metrics.SHA256 != hash {
		return nil, fault.New(fault.Validation, fault.StageValidate, tmpName, -1, -1, "hash measurement inconsistency")
	}

	recovered := false
	if _, statErr := os.Stat(final); statErr == nil {
		existingHash, hashErr := validate.HashFile(final)
		if hashErr != nil {
			return nil, hashErr
		}
		if existingHash != hash {
			return nil, fault.New(fault.Validation, fault.StageJournal, final, -1, -1, "existing artifact hash differs from rebuilt artifact")
		}
		// The crash happened after rename but before journal commit. Verify and adopt it.
		recovered = true
	} else if !os.IsNotExist(statErr) {
		return nil, fault.New(fault.IO, fault.StageJournal, final, -1, -1, "stat final artifact: %v", statErr)
	} else {
		if err := os.Rename(tmpName, final); err != nil {
			return nil, fault.New(fault.IO, fault.StageJournal, final, -1, -1, "atomic rename: %v", err)
		}
		if err := journal.SyncDirectory(store.DeliveryDir); err != nil {
			return nil, err
		}
		if in.CrashAfterRename {
			return nil, fault.New(fault.InjectedExit, fault.StageJournal, final, -1, -1, "injected exit after atomic rename and before journal commit")
		}
	}

	rec := journal.Record{
		Key: key, EventID: eventID, StartMS: in.StartMS, EndMS: in.EndMS,
		Artifact: rel, SHA256: hash,
	}
	if err := store.AppendRecord(rec); err != nil {
		return nil, err
	}
	manifest := journal.Manifest{
		Schema: "replay-manifest-v1", DeliveryKey: key, EventID: eventID,
		StartMS: in.StartMS, EndMS: in.EndMS, Artifact: rel, SHA256: hash,
		Metrics: *metrics, RecoveredRun: recovered,
	}
	if err := store.WriteManifest(manifest); err != nil {
		return nil, err
	}

	res := &Result{DeliveryKey: key, Artifact: rel, SHA256: hash, Metrics: metrics, Published: 1, Duplicate: 0}
	if recovered {
		res.Recovered = 1
	}
	return res, nil
}

func measureDelivery(path string, art *render.Artifact) (*validate.Metrics, error) {
	metrics, err := validate.MeasureFile(path)
	if err != nil {
		return nil, err
	}
	metrics.VideoAlignErrorMS = art.VideoAlignErrorMS
	metrics.AudioBoundaryErrorSamples = art.AudioBoundaryErrorSamples
	expected := validate.Expectations{
		Frames:                  art.Frames,
		Pixels:                  art.Pixels,
		AudioSamples:            art.AudioSamples,
		VideoAlignMaxMS:         20,
		AudioBoundaryMaxSamples: 1,
	}
	if err := metrics.Check(expected); err != nil {
		return nil, err
	}
	if !metrics.NearPeak() {
		return nil, fault.New(fault.Validation, fault.StageValidate, path, -1, -1, "peak %.2f dBFS is not near -12.04", metrics.PeakDBFS)
	}
	return metrics, nil
}
