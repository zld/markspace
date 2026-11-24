package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// resumeIfCommitted returns a summary for a run that already committed an
// output matching the checkpoint.  The input bytes and the committed payload
// digest must both still match, so a changed input or a damaged output forces
// a full rebuild instead of trusting stale state.
func resumeIfCommitted(workDir, outputPath string, data []byte) (Summary, bool) {
	cp, ok := loadCheckpoint(workDir)
	if !ok || cp.OutputSHA256 == "" || cp.InputSHA256 != sha256Hex(data) {
		return Summary{}, false
	}
	out, err := os.ReadFile(outputPath)
	if err != nil {
		return Summary{}, false
	}
	if OutputPayloadDigest(out) != cp.OutputSHA256 {
		return Summary{}, false
	}
	sum := Summary{
		Input: cp.Input, Output: outputPath, Resumed: true, Committed: true,
		EventsIn: len(cp.Events), EventsOut: len(cp.Events),
		EventTimescale: cp.EventTimescale, PayloadSHA256In: cp.OutputSHA256,
		PayloadSHA256Out: cp.OutputSHA256, OutputBytes: cp.OutputBytes,
	}
	sum.Tracks = len(cp.Tracks)
	for _, e := range cp.Tracks {
		sum.TrackIDs = append(sum.TrackIDs, e.InputID)
	}
	return sum, true
}

func loadCheckpoint(dir string) (*Checkpoint, bool) {
	buf, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if err != nil {
		return nil, false
	}
	var cp Checkpoint
	if err := json.Unmarshal(buf, &cp); err != nil {
		return nil, false
	}
	return &cp, true
}
