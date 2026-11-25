package pipeline

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"mediacore/internal/clock"
)

type validationArtifact struct {
	BatchID            string   `json:"batch_id"`
	Verified           bool     `json:"verified"`
	ParseRoundtripRate float64  `json:"parse_roundtrip_rate"`
	KeyframeHitRate    float64  `json:"keyframe_hit_rate"`
	SegmentHashes      []string `json:"segment_hashes"`
	TransformSHA256    string   `json:"transform_sha256"`
}

func (rn *Runner) runValidate() error {
	if math.Abs(rn.Report.ParseRoundtripRate-1.0) > 1e-12 {
		return clock.New(clock.CategoryInvalidArgument, rn.ManifestPath, 0, 0,
			"parse/event roundtrip rate is below 100%")
	}
	if math.Abs(rn.Report.KeyframeHitRate-1.0) > 1e-12 {
		return clock.New(clock.CategoryMissingKeyframe, rn.data.primary.Path, rn.data.primary.Track, 0,
			"keyframe hit rate is below 100%")
	}
	if rn.Report.QuerySampleCount == 0 || rn.Report.KeyframeNumber == 0 {
		return clock.New(clock.CategoryOutOfRange, rn.data.primary.Path, rn.data.primary.Track, 0,
			"validation query returned no keyframe/samples")
	}
	transformPath := filepath.Join(rn.WorkDir, "artifacts", "transform.json")
	transformHash, err := HashFile(transformPath)
	if err != nil {
		return clock.Wrap(clock.CategoryHashMismatch, transformPath, 0, 0, "hash transform artifact", err)
	}
	if st, ok := rn.State.Artifacts["transform"]; !ok || st.SHA256 != transformHash {
		return clock.New(clock.CategoryHashMismatch, transformPath, 0, 0,
			"transform artifact hash does not match committed state")
	}
	data, err := os.ReadFile(transformPath)
	if err != nil {
		return clock.Wrap(clock.CategoryIO, transformPath, 0, 0, "read transform for validation", err)
	}
	var got transformArtifact
	if err := json.Unmarshal(data, &got); err != nil {
		return clock.Wrap(clock.CategoryState, transformPath, 0, 0, "decode transform for validation", err)
	}
	if len(got.Segments) != len(rn.Report.SegmentHashes) {
		return clock.New(clock.CategoryHashMismatch, transformPath, 0, 0, "segment count changed during validation")
	}
	for i, seg := range got.Segments {
		if seg.ContentSHA256 != rn.Report.SegmentHashes[i] {
			return clock.New(clock.CategoryHashMismatch, transformPath, 0, seg.SampleOffsets[0],
				"segment hash changed during validation")
		}
	}
	payload := validationArtifact{
		BatchID: rn.BatchID, Verified: true,
		ParseRoundtripRate: rn.Report.ParseRoundtripRate,
		KeyframeHitRate:    rn.Report.KeyframeHitRate,
		SegmentHashes:      rn.Report.SegmentHashes,
		TransformSHA256:    transformHash,
	}
	if err := rn.materializeCommitted("validate", filepath.Join(rn.WorkDir, "artifacts", "validate.json"), payload); err != nil {
		return err
	}
	markerPath := filepath.Join(rn.WorkDir, "VERIFIED")
	if _, err := WriteAtomicVerified(markerPath, []byte("verified\n")); err != nil {
		return clock.Wrap(clock.CategoryIO, markerPath, 0, 0, "write verified marker", err)
	}
	return nil
}
