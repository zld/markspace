package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StageFunc computes a stage's output. It receives the run directory and can
// read earlier stages' committed output through in.PriorOutput. It must be
// pure with respect to the input and parameter digests: re-delivering the same
// inputs must produce byte-identical output.
type StageFunc func(in StageInput) (StageOutput, error)

// StageInput is the committed context available to a stage.
type StageInput struct {
	Stage        string
	InputDigest  string
	ParamsDigest string
	// Dir is the run output directory.
	Dir string
	// Prior is the output digest of the immediately preceding stage, empty
	// for the first stage.
	Prior string
}

// PriorOutput loads the committed output bytes of an earlier stage. On a
// resumed run those bytes are already on disk, so the stage never recomputes
// them.
func (in StageInput) PriorOutput(stage string) ([]byte, error) {
	return os.ReadFile(stagePath(in.Dir, stage))
}

// StageOutput is one stage's result.
type StageOutput struct {
	// Bytes is the stage's persisted output.
	Bytes []byte
	// Summary is an optional machine-readable contribution to the run
	// summary.
	Summary json.RawMessage
}

// stagePath is where a stage's output bytes live.
func stagePath(dir, stage string) string {
	return filepath.Join(dir, "stage-"+stage+".out")
}

// stageLabel identifies a stage in error messages.
func stageLabel(stage string) string { return fmt.Sprintf("stage %q", stage) }
