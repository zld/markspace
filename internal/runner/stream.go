package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"mediapack/internal/manifest"
)

// Config bounds execution so adversarial plans cannot exhaust resources.
type Config struct {
	WorkDir         string
	MaxSegments     int
	MaxSegmentBytes int64
	MaxOutputBytes  int64
}

// InputRef names one scanned input and its current digest, used to detect
// input changes before reusing a checkpoint.
type InputRef struct {
	Path   string
	SHA256 string
}

// Report is the deterministic result of one run.
type Report struct {
	Segments         []SegmentResult `json:"segments"`
	OutputSHA256     string          `json:"output_sha256"`
	ManifestSHA256   string          `json:"manifest_sha256"`
	CheckpointReused bool            `json:"checkpoint_reused"`
	ResumedFrom      int             `json:"resumed_from_segment"`
	ElapsedMS        int64           `json:"elapsed_ms"`
}

// Execute streams the plan into per-segment files, checkpoints after each
// verified segment and resumes only from a fully validated checkpoint. If the
// checkpoint or any completed segment fails verification, execution rolls back
// to the last safe boundary (the verified prefix) and redoes the remainder.
func Execute(plan *manifest.Plan, inputs []InputRef, cfg Config, startMS int64) (*Report, error) {
	if cfg.MaxSegments > 0 && len(plan.Segments) > cfg.MaxSegments {
		return nil, limitError(cfg.WorkDir, "segment count", int64(len(plan.Segments)), int64(cfg.MaxSegments))
	}
	segDir := filepath.Join(cfg.WorkDir, "segments")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		return nil, err
	}
	inputsDigest := digestInputs(inputs)
	cp, cpErr := LoadCheckpoint(cfg.WorkDir)
	reused := false
	startIdx := 0
	if cpErr != nil {
		// Corrupt checkpoint: discard and start from scratch (safe boundary 0).
		_ = discardCheckpoint(cfg.WorkDir)
		cp = nil
	}
	if cp != nil && cp.PlanHash == plan.ContentHash() && cp.InputsDigest == inputsDigest {
		if verr := cp.VerifyCompleted(); verr != nil {
			// Roll back to the verified prefix: keep every good segment, drop
			// the rest of the checkpoint, resume at the first bad index.
			keep := rollbackToSafePrefix(cp, segDir)
			cp.Completed = append([]SegmentResult{}, cp.Completed[:keep]...)
			cp.NextSegment = keep
			startIdx = keep
			_ = discardCheckpoint(cfg.WorkDir)
		} else {
			reused = true
			startIdx = cp.NextSegment
		}
	} else if cp != nil {
		_ = discardCheckpoint(cfg.WorkDir)
		cp = nil
	}

	results := make([]SegmentResult, 0, len(plan.Segments))
	if cp != nil {
		results = append(results, cp.Completed...)
	}
	var outputTotal int64
	for _, r := range results {
		outputTotal += r.Size
	}
	for i := startIdx; i < len(plan.Segments); i++ {
		seg := plan.Segments[i]
		res, err := writeSegment(segDir, seg, inputsDigest, cfg)
		if err != nil {
			return nil, err
		}
		outputTotal += res.Size
		if cfg.MaxOutputBytes > 0 && outputTotal > cfg.MaxOutputBytes {
			return nil, limitError(cfg.WorkDir, "output bytes", outputTotal, cfg.MaxOutputBytes)
		}
		results = append(results, res)
		cp := &Checkpoint{
			Version: CheckpointVersion, PlanHash: plan.ContentHash(),
			InputsDigest: inputsDigest, Completed: append([]SegmentResult{}, results...),
			NextSegment: i + 1,
		}
		if err := SaveCheckpoint(cfg.WorkDir, cp); err != nil {
			return nil, err
		}
	}

	manifestSum, err := writeManifest(cfg.WorkDir, plan)
	if err != nil {
		return nil, err
	}
	outSum, err := combineOutputs(cfg.WorkDir, results)
	if err != nil {
		return nil, err
	}
	finalCP := &Checkpoint{
		Version: CheckpointVersion, PlanHash: plan.ContentHash(),
		InputsDigest: inputsDigest, Completed: results, NextSegment: len(plan.Segments),
		OutputSHA256: outSum, ManifestSHA256: manifestSum,
	}
	if err := SaveCheckpoint(cfg.WorkDir, finalCP); err != nil {
		return nil, err
	}
	return &Report{
		Segments: results, OutputSHA256: outSum, ManifestSHA256: manifestSum,
		CheckpointReused: reused, ResumedFrom: startIdx,
		ElapsedMS: nowMS() - startMS,
	}, nil
}

// writeSegment streams one segment to a temp file, verifies its digest and
// atomically renames it into place. The payload is fully deterministic: a
// fixed header plus the segment metadata, no randomness or timestamps.
func writeSegment(dir string, seg manifest.Segment, inputsDigest string, cfg Config) (SegmentResult, error) {
	final := filepath.Join(dir, segmentFileName(seg.Index))
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return SegmentResult{}, err
	}
	h := sha256.New()
	mw := io.MultiWriter(f, h)
	payload := segmentPayload(seg, inputsDigest)
	if cfg.MaxSegmentBytes > 0 && int64(len(payload)) > cfg.MaxSegmentBytes {
		f.Close()
		_ = os.Remove(tmp)
		return SegmentResult{}, limitError(dir, "segment bytes", int64(len(payload)), cfg.MaxSegmentBytes)
	}
	if _, err := mw.Write(payload); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return SegmentResult{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return SegmentResult{}, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return SegmentResult{}, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if verify, _, err := hashFileBounded(tmp); err != nil || verify != sum {
		_ = os.Remove(tmp)
		return SegmentResult{}, cpError(tmp, "segment post-write verification failed")
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return SegmentResult{}, err
	}
	return SegmentResult{
		Index: seg.Index, Path: final, Size: int64(len(payload)),
		SHA256: sum, StartNS: seg.StartNS, EndNS: seg.EndNS,
	}, nil
}

// segmentPayload is the canonical, repeatable byte content of one segment.
func segmentPayload(seg manifest.Segment, inputsDigest string) []byte {
	type wire struct {
		Magic    string `json:"magic"`
		Index    int    `json:"index"`
		StartNS  int64  `json:"start_ns"`
		EndNS    int64  `json:"end_ns"`
		ActualNS int64  `json:"actual_ns"`
		Partial  bool   `json:"partial,omitempty"`
		Inputs   string `json:"inputs"`
	}
	raw, _ := json.Marshal(wire{
		Magic: "mediapack-segment-v1", Index: seg.Index, StartNS: seg.StartNS,
		EndNS: seg.EndNS, ActualNS: seg.ActualNS, Partial: seg.Partial,
		Inputs: inputsDigest,
	})
	return append(raw, '\n')
}

func segmentFileName(i int) string {
	return "seg-" + leftPad(i) + ".m4s"
}

func leftPad(i int) string {
	s := []byte{'0', '0', '0', '0', '0'}
	digits := []byte{}
	if i == 0 {
		digits = []byte{'0'}
	}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	copy(s[len(s)-len(digits):], digits)
	return string(s)
}

func digestInputs(inputs []InputRef) string {
	h := sha256.New()
	for _, in := range inputs {
		h.Write([]byte(in.Path))
		h.Write([]byte{0})
		h.Write([]byte(in.SHA256))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
