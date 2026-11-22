// Package verify independently checks a frames stream, its index and its
// manifest: the index must match the input, manifest statistics must be
// recomputable, and the timeline must align with the MP4 sample timing.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"os"

	"mediacore/index"
	"mediacore/manifest"
	"mediacore/media"
	"mediacore/pipeline"
)

// Input names the files to check.
type Input struct {
	FramesPath   string
	IndexPath    string
	ManifestPath string
}

// Report is the verifier output.
type Report struct {
	IndexMatchesInput bool                  `json:"index_matches_input"`
	IndexComplete     bool                  `json:"index_complete"`
	ManifestOK        bool                  `json:"manifest_ok"`
	RecomputedFrames  int                   `json:"recomputed_frames"`
	ManifestDigest    string                `json:"manifest_digest"`
	PipelineDigest    string                `json:"pipeline_digest"`
	Detail            manifest.VerifyReport `json:"detail"`
	Notes             []string              `json:"notes,omitempty"`
	Failures          []manifest.Failure    `json:"failures,omitempty"`
}

// Run verifies all three artifacts. Exit-code semantics are the caller's: a
// non-nil error means the material is damaged or statistics disagree (exit 1)
// unless it is a file/parameter problem (exit 2), signalled via ErrorCode.
func Run(in Input, opts manifest.VerifyOptions) (*Report, int, error) {
	rep := &Report{}
	frameBytes, err := os.ReadFile(in.FramesPath)
	if err != nil {
		return nil, 2, media.Wrap(media.CatIO, media.StageVerify, err, "read frames")
	}
	indexBytes, err := os.ReadFile(in.IndexPath)
	if err != nil {
		return nil, 2, media.Wrap(media.CatIO, media.StageVerify, err, "read index")
	}
	manifestBytes, err := os.ReadFile(in.ManifestPath)
	if err != nil {
		return nil, 2, media.Wrap(media.CatIO, media.StageVerify, err, "read manifest")
	}
	idx, err := index.UnmarshalIndex(indexBytes)
	if err != nil {
		return nil, 1, err
	}
	// The index must have been built from exactly these input bytes.
	if idx.InputDigest != digest(frameBytes) {
		rep.Failures = append(rep.Failures, manifest.Failure{
			Category: media.CatIndexInput, Field: "input_digest",
			Expected: idx.InputDigest, Actual: digest(frameBytes),
			Detail: "index was not built from this frame stream",
		})
		return rep, 1, media.New(media.CatIndexInput, media.StageVerify,
			"input digest mismatch")
	}
	rep.IndexMatchesInput = true
	rep.IndexComplete = idx.Complete
	if !idx.Complete {
		rep.Failures = append(rep.Failures, manifest.Failure{
			Category: media.CatIndexTruncated, Field: "complete",
			Expected: "true", Actual: "false",
			Detail: "index records an incomplete scan",
		})
		return rep, 1, media.New(media.CatIndexTruncated, media.StageVerify, "index is incomplete")
	}
	// Re-run the pipeline from scratch; it must reproduce the same counts and
	// a manifest digest equal to the stored one.
	res, err := pipeline.Run(idx, pipeline.NewBufferSource(frameBytes, media.DefaultLimits()),
		pipeline.DefaultConfig(media.DefaultTimescale))
	if err != nil {
		return nil, 1, err
	}
	rep.RecomputedFrames = res.FrameCount
	m, err := manifest.Parse(manifestBytes)
	if err != nil {
		return nil, 1, err
	}
	rep.ManifestDigest = m.ManifestDigest
	vr := manifest.Verify(m, opts)
	rep.Detail = vr
	if !vr.OK {
		rep.Failures = append(rep.Failures, vr.Failures...)
		return rep, 1, media.New(media.CatManifestMismatch, media.StageVerify,
			"manifest verification failed with %d issue(s)", len(vr.Failures))
	}
	rep.ManifestOK = true
	if m.Stats.FrameCount != res.FrameCount {
		rep.Failures = append(rep.Failures, manifest.Failure{
			Category: media.CatManifestMismatch, Field: "frame_count",
			Expected: itoa(res.FrameCount), Actual: itoa(m.Stats.FrameCount),
		})
		return rep, 1, media.New(media.CatManifestMismatch, media.StageVerify,
			"manifest frame count disagrees with a fresh scan")
	}
	// Rebuild the manifest from the fresh pipeline result and compare digests.
	rebound, err := manifest.Build(idx, res, manifest.Options{
		TargetKBPS: m.Stats.TargetKBPS,
		Bandwidth: &manifest.BandwidthReportLike{
			EstimateKBPS:     m.Bandwidth.EstimateKBPS,
			InputCeilingKBPS: m.Bandwidth.InputCeilingKBPS,
			TargetKBPS:       m.Stats.TargetKBPS,
			Reason:           m.Bandwidth.Reason,
		},
		Timing: m.Timing,
	})
	if err != nil {
		return nil, 1, err
	}
	rep.PipelineDigest = rebound.ManifestDigest
	if rebound.ManifestDigest != m.ManifestDigest {
		rep.Failures = append(rep.Failures, manifest.Failure{
			Category: media.CatManifestMismatch, Field: "manifest_digest",
			Expected: m.ManifestDigest, Actual: rebound.ManifestDigest,
			Detail: "reprocessing the index did not reproduce the manifest",
		})
		return rep, 1, media.New(media.CatManifestMismatch, media.StageVerify,
			"manifest digest is not reproducible")
	}
	return rep, 0, nil
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func itoa(v int) string { return manifest.Itoa(int64(v)) }
