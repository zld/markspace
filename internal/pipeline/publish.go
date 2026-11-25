package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediacore/internal/clock"
)

// Publication is the deterministic idempotent output manifest.
type Publication struct {
	BatchID          string   `json:"batch_id"`
	KeyframeNumber   int      `json:"keyframe_number"`
	QuerySampleCount int      `json:"query_sample_count"`
	SegmentHashes    []string `json:"segment_hashes"`
	ValidationSHA256 string   `json:"validation_sha256"`
}

func (rn *Runner) runPublish() error {
	validation, ok := rn.State.Artifacts["validate"]
	if !ok || validation.SHA256 == "" {
		return clock.New(clock.CategoryState, rn.Store.path, 0, 0, "cannot publish before validated state")
	}
	pub := Publication{
		BatchID: rn.BatchID, KeyframeNumber: rn.Report.KeyframeNumber,
		QuerySampleCount: rn.Report.QuerySampleCount,
		SegmentHashes:    append([]string(nil), rn.Report.SegmentHashes...),
		ValidationSHA256: validation.SHA256,
	}
	data, err := json.MarshalIndent(pub, "", "  ")
	if err != nil {
		return clock.Wrap(clock.CategoryState, rn.Store.path, 0, 0, "encode publication", err)
	}
	want := hashBytes(data)
	path := filepath.Join(rn.WorkDir, "published", "publication.json")
	existing, hashErr := HashFile(path)
	fileMatches := hashErr == nil && existing == want
	stateMatches := false
	if art, ok := rn.State.Artifacts["publish"]; ok && art.SHA256 == want && rn.State.Published {
		stateMatches = true
	}

	switch {
	case fileMatches && stateMatches:
		// Exact no-op rerun. No byte is published twice.
		return nil
	case fileMatches && !stateMatches:
		// Product is durable but the commit was interrupted: adopt and backfill state.
		rn.Report.RecoveredArtifacts++
		rn.State.complete("publish", ArtifactState{
			Stage: "publish", Path: path, SHA256: want, Verified: true,
		})
		rn.State.Published = true
		return rn.Store.Save(rn.State)
	default:
		if _, committed := rn.State.Artifacts["publish"]; committed {
			rn.Report.RegeneratedArtifacts++
		}
		if hashErr != nil && !os.IsNotExist(hashErr) {
			return clock.Wrap(clock.CategoryIO, path, 0, 0, "hash existing publication", hashErr)
		}
		got, err := WriteAtomicVerified(path, data)
		if err != nil {
			return err
		}
		rn.Report.PublishWrites++
		rn.State.complete("publish", ArtifactState{
			Stage: "publish", Path: path, SHA256: got, Verified: true,
		})
		if rn.CrashAfter == "publish" {
			// Durable product, deliberately before the atomic state commit.
			return ErrSimulatedCrash
		}
		rn.State.Published = true
		return rn.Store.Save(rn.State)
	}
}
