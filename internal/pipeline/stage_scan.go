package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediacore/internal/bitstream"
	"mediacore/internal/fault"
)

// stageScan scans the encoded elementary stream and validates its keyframe
// index. Any quarantined row rejects the run before it can be archived.
func stageScan(rep *Report, opts Options, mediaDir, encoded string) ([]byte, *bitstream.Stream, error) {
	rawBytes, err := os.ReadFile(encoded)
	if err != nil {
		return nil, nil, fault.Newf(fault.CodeInternal, "", "cannot read encoded stream: %v", err)
	}
	stream, err := bitstream.Scan(opts.Codec, rawBytes)
	if err != nil {
		rep.Decision = "reject"
		rep.DecisionReason = err.Error()
		rep.computeDigest()
		return nil, nil, err
	}
	rep.ScanFrames = len(stream.Frames)
	idx := buildIndex(stream)
	if opts.CorruptIndex {
		corruptIndex(idx)
	}
	idxBytes, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(mediaDir, "keyframes.json"), idxBytes, 0o644); err != nil {
		return nil, nil, err
	}
	parsedIdx, err := bitstream.ParseIndex(idxBytes)
	if err != nil {
		rep.Decision = "reject"
		rep.DecisionReason = err.Error()
		rep.computeDigest()
		return nil, nil, err
	}
	idxReport, err := bitstream.ValidateIndex(stream, parsedIdx)
	if err != nil {
		rep.Decision = "reject"
		rep.DecisionReason = err.Error()
		rep.computeDigest()
		return nil, nil, err
	}
	rep.Keyframes = len(idxReport.AcceptedKeyframeOffsets())
	rep.IndexQuarantined = idxReport.QuarantinedCount()
	if rep.IndexQuarantined > 0 {
		q := idxReport.Quarantined[0]
		rep.Decision = "reject"
		rep.DecisionReason = q.Fault.Error()
		rep.computeDigest()
		return nil, nil, q.Fault
	}
	return rawBytes, stream, nil
}
