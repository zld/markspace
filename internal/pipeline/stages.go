package pipeline

import (
	"encoding/json"

	"mediad/internal/checkpoint"
	"mediad/internal/clock"
	"mediad/internal/index"
	"mediad/internal/manifest"
	mp4 "mediad/internal/mp4"
	"mediad/internal/provenance"
)

// payload wraps a stage's serialized output with a schema tag so a future
// format change is detected as a mismatch rather than misdecoded. No timing
// or path data is stored: the payload feeds the deterministic output digest.
type payload[T any] struct {
	Schema string `json:"schema"`
	Value  T      `json:"value"`
}

const schemaV1 = "v1"

// loadOrScan reuses a checkpointed scan when valid, otherwise scans.
func loadOrScan(store *checkpoint.Store, opts Options, man *manifest.Manifest, key checkpoint.Key, mustRun bool) (map[string]*mp4.Scan, bool, error) {
	if !mustRun {
		if e, err := store.Load(checkpoint.StageScan); err == nil && e.Match(key) {
			var p payload[map[string]*mp4.Scan]
			if err := json.Unmarshal(e.Payload, &p); err == nil && p.Schema == schemaV1 {
				return p.Value, true, nil
			}
			// A payload we cannot decode is corrupt: invalidate and rescan.
			if err := store.Invalidate(checkpoint.StageScan); err != nil {
				return nil, false, err
			}
		}
	}
	scans, err := scanFiles(man)
	if err != nil {
		return nil, false, err
	}
	if err := saveStage(store, checkpoint.StageScan, key, payload[map[string]*mp4.Scan]{Schema: schemaV1, Value: scans}); err != nil {
		return nil, false, err
	}
	return scans, false, nil
}

func loadOrRepair(store *checkpoint.Store, opts Options, man *manifest.Manifest, scans map[string]*mp4.Scan, key checkpoint.Key, mustRun bool) (map[string]*mp4.Repaired, bool, error) {
	if !mustRun {
		if e, err := store.Load(checkpoint.StageRepair); err == nil && e.Match(key) {
			var p payload[map[string]*mp4.Repaired]
			if err := json.Unmarshal(e.Payload, &p); err == nil && p.Schema == schemaV1 {
				return p.Value, true, nil
			}
			if err := store.Invalidate(checkpoint.StageRepair); err != nil {
				return nil, false, err
			}
		}
	}
	reps, err := repairAll(man, scans, opts)
	if err != nil {
		return nil, false, err
	}
	if err := saveStage(store, checkpoint.StageRepair, key, payload[map[string]*mp4.Repaired]{Schema: schemaV1, Value: reps}); err != nil {
		return nil, false, err
	}
	return reps, false, nil
}

func loadOrIndex(store *checkpoint.Store, opts Options, repairs map[string]*mp4.Repaired, key checkpoint.Key, mustRun bool) (map[string]*index.Index, bool, error) {
	if !mustRun {
		if e, err := store.Load(checkpoint.StageIndex); err == nil && e.Match(key) {
			var p payload[map[string]*index.Index]
			if err := json.Unmarshal(e.Payload, &p); err == nil && p.Schema == schemaV1 {
				return p.Value, true, nil
			}
			if err := store.Invalidate(checkpoint.StageIndex); err != nil {
				return nil, false, err
			}
		}
	}
	idxs, err := buildIndexes(repairs, opts)
	if err != nil {
		return nil, false, err
	}
	if err := saveStage(store, checkpoint.StageIndex, key, payload[map[string]*index.Index]{Schema: schemaV1, Value: idxs}); err != nil {
		return nil, false, err
	}
	return idxs, false, nil
}

func saveStage(store *checkpoint.Store, stage checkpoint.Stage, key checkpoint.Key, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return clock.New(clock.StageCheckpoint, clock.CodeIO, "marshal %s payload: %v", stage, err).WithCause(err)
	}
	sum, _, err := provenance.HashJSON(value)
	if err != nil {
		return err
	}
	return store.Save(&checkpoint.Entry{
		Stage: stage, InputSHA: key.InputSHA, ParamSHA: key.ParamSHA,
		StageVer: key.StageVer, OutputSHA: sum, Payload: raw,
	})
}

func recordStage(store *checkpoint.Store, prov *provenance.Record, stage checkpoint.Stage, key checkpoint.Key, value any, resumed bool, detail string) error {
	sum, _, err := provenance.HashJSON(value)
	if err != nil {
		return err
	}
	prov.Stages = append(prov.Stages, provenance.StageRecord{
		Stage: string(stage), Version: key.StageVer, InputSHA: key.InputSHA,
		ParamSHA: key.ParamSHA, OutputSHA: sum, Status: "complete",
		Resumed: resumed, Detail: detail,
	})
	return nil
}
